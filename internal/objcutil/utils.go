// Port of tart's Utils.swift, plus the Go↔Foundation bridge helpers shared by
// every file in this package.
//go:build darwin

package objcutil

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc/objcerrors"
)

var (
	SelObjectForKey  = objc.RegisterName("objectForKey:")
	SelCount         = objc.RegisterName("count")
	SelObjectAtIndex = objc.RegisterName("objectAtIndex:")
	SelAddObject     = objc.RegisterName("addObject:")
	SelArray         = objc.RegisterName("array")
)

// WrapperID recovers the raw ObjC pointer from a generated-binding value
// whose generic instantiation returned the object address as a Go wrapper
// pointer (e.g. ResourceValuesForKeysError); such values must never have
// their methods called directly.
func WrapperID[T any](p *T) objc.ID {
	return objc.ID(uintptr(unsafe.Pointer(p)))
}

// NSStr converts a Go string to a Foundation NSString.
func NSStr(s string) *foundation.NSString {
	return foundation.NSStringStringWithUTF8String(s)
}

// GoStr converts a Foundation NSString to a Go string.
func GoStr(s *foundation.NSString) string {
	if s == nil {
		return ""
	}
	return pureobjc.GoString(s.Ptr())
}

// EnvironmentValue mirrors Swift's ProcessInfo.processInfo.environment[name],
// going through NSProcessInfo rather than os.Getenv.
func EnvironmentValue(name string) (string, bool) {
	environment := foundation.NSProcessInfoProcessInfo().Environment()
	value := objc.Send[objc.ID](environment.Ptr(), SelObjectForKey, pureobjc.NSString(name))
	if value == 0 {
		return "", false
	}
	return pureobjc.GoString(value), true
}

// The generic NSArray accessors instantiate objc.Send with wrapper-pointer
// type parameters, which is not ABI-safe through purego, so the helpers below
// iterate containers with direct sends and rewrap each element via FromID
// (retaining first, because FromID registers a releasing finalizer).

// NSArrayURLs converts an NSArray<NSURL *> to a Go slice.
func NSArrayURLs(array *foundation.NSArray[*foundation.NSURL]) []*foundation.NSURL {
	if array == nil {
		return nil
	}
	count := objc.Send[uint](array.Ptr(), SelCount)
	urls := make([]*foundation.NSURL, 0, count)
	for i := range count {
		id := objc.Send[objc.ID](array.Ptr(), SelObjectAtIndex, i)
		urls = append(urls, foundation.NSURLFromID(pureobjc.Retain(id)))
	}
	return urls
}

// NSArrayStrings converts an NSArray<NSString *> to a Go slice of strings.
func NSArrayStrings(array *foundation.NSArray[*foundation.NSString]) []string {
	if array == nil {
		return nil
	}
	count := objc.Send[uint](array.Ptr(), SelCount)
	strs := make([]string, 0, count)
	for i := range count {
		id := objc.Send[objc.ID](array.Ptr(), SelObjectAtIndex, i)
		strs = append(strs, pureobjc.GoString(id))
	}
	return strs
}

// EmptyNSArray builds an empty NSArray typed for use as a generated-binding
// parameter (e.g. includingPropertiesForKeys:), which cannot be nil because
// the generated body dereferences it.
func EmptyNSArray[T any]() *foundation.NSArray[T] {
	empty := foundation.NSArrayArray()
	return foundation.NSArrayFromID[T](pureobjc.Retain(empty.Ptr()))
}

// AllocClass sends +alloc to the named class, for use with the generated
// Init* instance methods.
func AllocClass(className string) objc.ID {
	return objc.Send[objc.ID](objc.ID(objc.GetClass(className)), objc.RegisterName("alloc"))
}

// NSArrayFromIDs builds a typed NSArray from raw ObjC object pointers.
func NSArrayFromIDs[T any](ids ...objc.ID) *foundation.NSArray[T] {
	array := objc.Send[objc.ID](objc.ID(objc.GetClass("NSMutableArray")), SelArray)
	for _, id := range ids {
		array.Send(SelAddObject, id)
	}
	return foundation.NSArrayFromID[T](pureobjc.Retain(array))
}

// NSStringArray converts a Go string slice to an NSArray<NSString *>.
func NSStringArray(items []string) *foundation.NSArray[*foundation.NSString] {
	ids := make([]objc.ID, 0, len(items))
	for _, item := range items {
		ids = append(ids, pureobjc.NSString(item))
	}
	return NSArrayFromIDs[*foundation.NSString](ids...)
}

// NSDataToBytes copies an NSData's contents into a Go byte slice.
func NSDataToBytes(data *foundation.NSData) []byte {
	if data == nil {
		return nil
	}
	length := data.Length()
	if length == 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice((*byte)(data.Bytes()), length)...)
}

// BytesToNSData copies a Go byte slice into a new NSData.
func BytesToNSData(b []byte) *foundation.NSData {
	if len(b) == 0 {
		return foundation.NSDataDataWithBytesLength(nil, 0)
	}
	return foundation.NSDataDataWithBytesLength(unsafe.Pointer(&b[0]), uint(len(b)))
}

// URLResourceValue fetches a single NSURL resource value (Swift:
// resourceValues(forKeys:)), returning the raw object or 0 when absent.
// keyID is the raw ObjC pointer of an NSURLResourceKey — for the generated
// extern accessors that is WrapperID(foundation.NSURL…Key()), because those
// return the object address cast to *NSString rather than a real wrapper.
func URLResourceValue(url *foundation.NSURL, keyID objc.ID) (objc.ID, error) {
	keys := NSArrayFromIDs[*foundation.NSString](keyID)
	values, err := url.ResourceValuesForKeysError(keys)
	if err != nil {
		return 0, err
	}
	if values == nil {
		return 0, nil
	}
	return objc.Send[objc.ID](WrapperID(values), SelObjectForKey, keyID), nil
}

// IsURLError reports whether err is an NSURLErrorDomain error — the Go
// equivalent of Swift's `error is URLError` checks.
func IsURLError(err error) bool {
	var objcErr *objcerrors.ObjCError
	return errors.As(err, &objcErr) && objcErr.Domain == "NSURLErrorDomain"
}

// RetryOnURLError ports the Retry package usage: retry fn up to maxAttempts
// times, but only when the failure is a URL (network) error.
func RetryOnURLError(maxAttempts int, fn func() error) error {
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !IsURLError(err) {
			return err
		}
		fmt.Printf("Error: %v\nAttempting to re-try...\n", err)
	}
	return err
}

// SafeIndex ports tart's Collection subscript(safe:) extension.
func SafeIndex[T any](collection []T, index int) (T, bool) {
	var zero T
	if index < 0 || index >= len(collection) {
		return zero, false
	}
	return collection[index], true
}

// ResolveBinaryPath ports tart's ResolveBinaryPath: it walks $PATH and
// returns the URL of the first entry containing a file called name, or nil.
func ResolveBinaryPath(name string) *foundation.NSURL {
	path, ok := EnvironmentValue("PATH")
	if !ok {
		return nil
	}

	fileManager := foundation.NSFileManagerDefaultManager()
	for pathComponent := range strings.SplitSeq(path, ":") {
		url := foundation.NSURLFileURLWithPath(NSStr(pathComponent)).
			URLByAppendingPathComponentIsDirectory(NSStr(name), false)

		if fileManager.FileExistsAtPath(url.Path()) {
			return url
		}
	}

	return nil
}

// TextPreview ports Data.asTextPreview(limit:).
func TextPreview(data []byte) string {
	const limit = 1000
	if len(data) <= limit {
		return string(data)
	}
	return string(data[:limit]) + "..."
}

// ExpandTilde ports NSString.expandingTildeInPath.
func ExpandTilde(path string) string {
	return GoStr(NSStr(path).StringByExpandingTildeInPath())
}

// NSURLFromPath builds a file NSURL from a Go path string.
func NSURLFromPath(path string) *foundation.NSURL {
	return foundation.NSURLFileURLWithPath(NSStr(path))
}

var (
	SelDictionary      = objc.RegisterName("dictionary")
	SelSetObjectForKey = objc.RegisterName("setObject:forKey:")
)
