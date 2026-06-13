// Port of tart's Credentials/KeychainCredentialsProvider.swift, using the
// Security framework bindings. CFDictionary/CFString/CFData are toll-free
// bridged to their NS counterparts, so queries are built as
// NSMutableDictionary via the ObjC runtime and passed as CFTypeRefs.
//go:build darwin

package credentials

import (
	"strconv"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/security"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

const (
	errSecSuccess      = 0
	errSecItemNotFound = -25300
)

const keychainCredentialsLabel = "Weave Credentials"

// KeychainCredentialsProvider ports the class of the same name.
type KeychainCredentialsProvider struct{}

var _ CredentialsProvider = (*KeychainCredentialsProvider)(nil)

func (p *KeychainCredentialsProvider) UserFriendlyName() string {
	return "Keychain credentials provider"
}

var (
	selDictionary      = objc.RegisterName("dictionary")
	selSetObjectForKey = objc.RegisterName("setObject:forKey:")
)

func newMutableDictionary() objc.ID {
	return objc.Send[objc.ID](objc.ID(objc.GetClass("NSMutableDictionary")), selDictionary)
}

// secConstant dereferences a kSec* extern symbol address (as returned by the
// generated raw accessors) to the CFTypeRef constant it points at.
func secConstant(symbolAddr uintptr) objc.ID {
	if symbolAddr == 0 {
		return 0
	}
	return *(*objc.ID)(launderPointer(symbolAddr))
}

// launderPointer converts a C symbol/object address held in a uintptr to an
// unsafe.Pointer. The indirection exists because these addresses originate
// from dlsym/the ObjC runtime — never from Go-managed memory — which go
// vet's unsafeptr check cannot know.
func launderPointer(addr uintptr) unsafe.Pointer {
	p := unsafe.Pointer(nil)
	*(*uintptr)(unsafe.Pointer(&p)) = addr
	return p
}

// idPointer passes an ObjC object id as a CFTypeRef-style unsafe.Pointer.
func idPointer(id objc.ID) unsafe.Pointer {
	return launderPointer(uintptr(id))
}

func dictSet(dict objc.ID, keySymbolAddr uintptr, value objc.ID) {
	dict.Send(selSetObjectForKey, value, secConstant(keySymbolAddr))
}

func nsNumberWithBool(value bool) objc.ID {
	return objc.Send[objc.ID](objc.ID(objc.GetClass("NSNumber")), objc.RegisterName("numberWithBool:"), value)
}

func (p *KeychainCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	query := newMutableDictionary()
	dictSet(query, security.KSecClass(), secConstant(security.KSecClassInternetPassword()))
	dictSet(query, security.KSecAttrProtocol(), secConstant(security.KSecAttrProtocolHTTPS()))
	dictSet(query, security.KSecAttrServer(), pureobjc.NSString(host))
	dictSet(query, security.KSecMatchLimit(), secConstant(security.KSecMatchLimitOne()))
	dictSet(query, security.KSecReturnAttributes(), nsNumberWithBool(true))
	dictSet(query, security.KSecReturnData(), nsNumberWithBool(true))
	dictSet(query, security.KSecAttrLabel(), pureobjc.NSString(keychainCredentialsLabel))

	var item uintptr
	status := security.SecItemCopyMatching(idPointer(query), unsafe.Pointer(&item))

	if status != errSecSuccess {
		if status == errSecItemNotFound {
			return "", "", false, nil
		}
		return "", "", false, credentialsProviderFailed("Keychain returned unsuccessful status %d", status)
	}

	itemID := objc.ID(item)
	userID := objc.Send[objc.ID](itemID, objcutil.SelObjectForKey, secConstant(security.KSecAttrAccount()))
	passwordDataID := objc.Send[objc.ID](itemID, objcutil.SelObjectForKey, secConstant(security.KSecValueData()))
	if userID == 0 || passwordDataID == 0 {
		return "", "", false, credentialsProviderFailed("Keychain item has unexpected format")
	}

	user := pureobjc.GoString(userID)
	passwordLength := objc.Send[uint](passwordDataID, objc.RegisterName("length"))
	passwordBytes := objc.Send[unsafe.Pointer](passwordDataID, objc.RegisterName("bytes"))
	password := string(unsafe.Slice((*byte)(passwordBytes), passwordLength))

	return user, password, true, nil
}

func (p *KeychainCredentialsProvider) Store(host string, user string, password string) error {
	key := newMutableDictionary()
	dictSet(key, security.KSecClass(), secConstant(security.KSecClassInternetPassword()))
	dictSet(key, security.KSecAttrProtocol(), secConstant(security.KSecAttrProtocolHTTPS()))
	dictSet(key, security.KSecAttrServer(), pureobjc.NSString(host))
	dictSet(key, security.KSecAttrLabel(), pureobjc.NSString(keychainCredentialsLabel))

	value := newMutableDictionary()
	dictSet(value, security.KSecAttrAccount(), pureobjc.NSString(user))
	dictSet(value, security.KSecValueData(), objcutil.BytesToNSData([]byte(password)).Ptr())

	status := security.SecItemCopyMatching(idPointer(key), nil)

	switch status {
	case errSecItemNotFound:
		merged := newMutableDictionary()
		merged.Send(objc.RegisterName("addEntriesFromDictionary:"), key)
		merged.Send(objc.RegisterName("addEntriesFromDictionary:"), value)
		if status := security.SecItemAdd(idPointer(merged), nil); status != errSecSuccess {
			return credentialsProviderFailed("Keychain failed to add item: %s", secStatusExplanation(status))
		}
	case errSecSuccess:
		if status := security.SecItemUpdate(idPointer(key), idPointer(value)); status != errSecSuccess {
			return credentialsProviderFailed("Keychain failed to update item: %s", secStatusExplanation(status))
		}
	default:
		return credentialsProviderFailed("Keychain failed to find item: %s", secStatusExplanation(status))
	}

	return nil
}

// Remove ports KeychainCredentialsProvider.remove(host:).
func (p *KeychainCredentialsProvider) Remove(host string) error {
	query := newMutableDictionary()
	dictSet(query, security.KSecClass(), secConstant(security.KSecClassInternetPassword()))
	dictSet(query, security.KSecAttrServer(), pureobjc.NSString(host))
	dictSet(query, security.KSecAttrLabel(), pureobjc.NSString(keychainCredentialsLabel))

	switch status := security.SecItemDelete(idPointer(query)); status {
	case errSecSuccess, errSecItemNotFound:
		return nil
	default:
		return credentialsProviderFailed("Failed to remove Keychain item(s): %s", secStatusExplanation(status))
	}
}

// secStatusExplanation ports the OSStatus.explanation() extension.
func secStatusExplanation(status int) string {
	message := security.SecCopyErrorMessageString(status, nil)
	if message == nil {
		return "Unknown status code " + strconv.Itoa(status)
	}
	return pureobjc.GoString(objc.ID(uintptr(message)))
}
