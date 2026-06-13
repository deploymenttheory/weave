// Port of tart's Credentials/KeychainCredentialsProvider.swift, using the
// Security framework bindings. CFDictionary/CFString/CFData are toll-free
// bridged to their NS counterparts, so queries are built as
// NSMutableDictionary via the ObjC runtime and passed as CFTypeRefs.
//go:build darwin

package credentials

import (
	"strconv"
	"unsafe"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/security"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
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
	selDictionary      = purego.RegisterName("dictionary")
	selSetObjectForKey = purego.RegisterName("setObject:forKey:")
)

func newMutableDictionary() purego.ID {
	return purego.Send[purego.ID](purego.ID(purego.GetClass("NSMutableDictionary")), selDictionary)
}

// secConstant dereferences a kSec* extern symbol address (as returned by the
// generated raw accessors) to the CFTypeRef constant it points at.
func secConstant(symbolAddr uintptr) purego.ID {
	if symbolAddr == 0 {
		return 0
	}
	return *(*purego.ID)(launderPointer(symbolAddr))
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
func idPointer(id purego.ID) unsafe.Pointer {
	return launderPointer(uintptr(id))
}

func dictSet(dict purego.ID, keySymbolAddr uintptr, value purego.ID) {
	dict.Send(selSetObjectForKey, value, secConstant(keySymbolAddr))
}

func nsNumberWithBool(value bool) purego.ID {
	return purego.Send[purego.ID](purego.ID(purego.GetClass("NSNumber")), purego.RegisterName("numberWithBool:"), value)
}

func (p *KeychainCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	query := newMutableDictionary()
	dictSet(query, security.KSecClass(), secConstant(security.KSecClassInternetPassword()))
	dictSet(query, security.KSecAttrProtocol(), secConstant(security.KSecAttrProtocolHTTPS()))
	dictSet(query, security.KSecAttrServer(), purego.NSString(host))
	dictSet(query, security.KSecMatchLimit(), secConstant(security.KSecMatchLimitOne()))
	dictSet(query, security.KSecReturnAttributes(), nsNumberWithBool(true))
	dictSet(query, security.KSecReturnData(), nsNumberWithBool(true))
	dictSet(query, security.KSecAttrLabel(), purego.NSString(keychainCredentialsLabel))

	var item uintptr
	status := security.SecItemCopyMatching(idPointer(query), unsafe.Pointer(&item))

	if status != errSecSuccess {
		if status == errSecItemNotFound {
			return "", "", false, nil
		}
		return "", "", false, credentialsProviderFailed("Keychain returned unsuccessful status %d", status)
	}

	itemID := purego.ID(item)
	userID := purego.Send[purego.ID](itemID, objcutil.SelObjectForKey, secConstant(security.KSecAttrAccount()))
	passwordDataID := purego.Send[purego.ID](itemID, objcutil.SelObjectForKey, secConstant(security.KSecValueData()))
	if userID == 0 || passwordDataID == 0 {
		return "", "", false, credentialsProviderFailed("Keychain item has unexpected format")
	}

	user := purego.GoString(userID)
	passwordLength := purego.Send[uint](passwordDataID, purego.RegisterName("length"))
	passwordBytes := purego.Send[unsafe.Pointer](passwordDataID, purego.RegisterName("bytes"))
	password := string(unsafe.Slice((*byte)(passwordBytes), passwordLength))

	return user, password, true, nil
}

func (p *KeychainCredentialsProvider) Store(host string, user string, password string) error {
	key := newMutableDictionary()
	dictSet(key, security.KSecClass(), secConstant(security.KSecClassInternetPassword()))
	dictSet(key, security.KSecAttrProtocol(), secConstant(security.KSecAttrProtocolHTTPS()))
	dictSet(key, security.KSecAttrServer(), purego.NSString(host))
	dictSet(key, security.KSecAttrLabel(), purego.NSString(keychainCredentialsLabel))

	value := newMutableDictionary()
	dictSet(value, security.KSecAttrAccount(), purego.NSString(user))
	dictSet(value, security.KSecValueData(), objcutil.BytesToNSData([]byte(password)).Ptr())

	status := security.SecItemCopyMatching(idPointer(key), nil)

	switch status {
	case errSecItemNotFound:
		merged := newMutableDictionary()
		merged.Send(purego.RegisterName("addEntriesFromDictionary:"), key)
		merged.Send(purego.RegisterName("addEntriesFromDictionary:"), value)
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
	dictSet(query, security.KSecAttrServer(), purego.NSString(host))
	dictSet(query, security.KSecAttrLabel(), purego.NSString(keychainCredentialsLabel))

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
	return purego.GoString(purego.ID(uintptr(message)))
}
