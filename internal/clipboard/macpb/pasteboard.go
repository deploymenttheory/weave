// Package macpb is the macOS NSPasteboard read/write shared by the host engine
// and the guest agent's darwin backend. It is threading-agnostic: callers that
// need main-thread affinity (the host engine) wrap these calls in
// objc.RunOnMainThread; the guest CLI agent calls them directly.
//go:build darwin

package macpb

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/appkit"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
	"github.com/deploymenttheory/weave/internal/clipboard/wire"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/ebitengine/purego/objc"
)

// FileURLType is the UTI for a single file reference on the pasteboard.
const FileURLType = "public.file-url"

var errWriteObjects = errors.New("NSPasteboard writeObjects failed")

// Collection-returning selectors are sent raw (objc.Send to objc.ID) rather
// than through the generated typed accessors: the generated NSArray return
// values are not ABI-safe through purego (they produce fake wrapper pointers,
// the same reason objcutil.NSArrayStrings/NSArrayURLs exist). Sending raw and
// iterating with SelCount/SelObjectAtIndex yields real element pointers.
var (
	selTypes           = objc.RegisterName("types")
	selPasteboardItems = objc.RegisterName("pasteboardItems")
)

// ChangeCount returns NSPasteboard's general change counter.
func ChangeCount() uint64 {
	return uint64(appkit.NSPasteboardGeneralPasteboard().ChangeCount())
}

// Read captures the general pasteboard restricted to the allowed canonical
// formats. maxBytes drops any single item/file larger than the cap (0 =
// unlimited). The file channel is read only when wire.CanonFiles is allowed.
func Read(allowed map[wire.Canonical]bool, maxBytes int64) wire.Payload {
	pb := appkit.NSPasteboardGeneralPasteboard()
	var payload wire.Payload

	seen := map[wire.Canonical]bool{}
	for _, uti := range pasteboardTypeUTIs(pb) {
		canon, ok := wire.CanonicalForUTI(uti)
		if !ok || !allowed[canon] || seen[canon] || canon == wire.CanonFiles {
			continue
		}
		data := objcutil.NSDataToBytes(pb.DataForType(objcutil.NSStr(uti)))
		if len(data) == 0 || tooBig(int64(len(data)), maxBytes) {
			continue
		}
		payload.Items = append(payload.Items, wire.DataItem{Format: canon, Data: data})
		seen[canon] = true
	}

	if allowed[wire.CanonFiles] {
		for _, item := range pasteboardItems(pb) {
			path := filePathFromURL(objcutil.GoStr(item.StringForType(objcutil.NSStr(FileURLType))))
			if path == "" {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil || tooBig(int64(len(data)), maxBytes) {
				continue
			}
			payload.Files = append(payload.Files, wire.DataFile{Name: filepath.Base(path), Data: data})
		}
	}

	return payload
}

// Write replaces the general pasteboard with the payload. Non-file
// representations are combined into a single pasteboard item; each file becomes
// its own item, staged under stageDir and referenced by a file URL.
func Write(p wire.Payload, stageDir string) error {
	pb := appkit.NSPasteboardGeneralPasteboard()
	itemIDs := make([]objc.ID, 0, len(p.Files)+1)

	if len(p.Items) > 0 {
		item := newPasteboardItem()
		for _, di := range p.Items {
			uti, ok := wire.UTIForCanonical(di.Format)
			if !ok {
				continue
			}
			item.SetDataForType(objcutil.BytesToNSData(di.Data), objcutil.NSStr(uti))
		}
		itemIDs = append(itemIDs, item.Ptr())
	}

	for _, file := range p.Files {
		path := filepath.Join(stageDir, file.Name)
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			return err
		}
		item := newPasteboardItem()
		item.SetStringForType(objcutil.NSStr((&url.URL{Scheme: "file", Path: path}).String()), objcutil.NSStr(FileURLType))
		itemIDs = append(itemIDs, item.Ptr())
	}

	pb.ClearContents()
	if len(itemIDs) == 0 {
		return nil
	}
	if !pb.WriteObjects(objcutil.NSArrayFromIDs[appkit.NSPasteboardWriting](itemIDs...)) {
		return errWriteObjects
	}
	return nil
}

func tooBig(n, maxBytes int64) bool { return maxBytes > 0 && n > maxBytes }

// pasteboardTypeUTIs returns the UTIs currently on the pasteboard via a raw
// send (see selTypes).
func pasteboardTypeUTIs(pb *appkit.NSPasteboard) []string {
	array := objc.Send[objc.ID](pb.Ptr(), selTypes)
	if array == 0 {
		return nil
	}
	count := objc.Send[uint](array, objcutil.SelCount)
	utis := make([]string, 0, count)
	for i := range count {
		id := objc.Send[objc.ID](array, objcutil.SelObjectAtIndex, i)
		utis = append(utis, pureobjc.GoString(id))
	}
	return utis
}

// pasteboardItems returns the pasteboard's items via a raw send (see
// selPasteboardItems), each rewrapped after retaining (FromID registers a
// releasing finalizer).
func pasteboardItems(pb *appkit.NSPasteboard) []*appkit.NSPasteboardItem {
	array := objc.Send[objc.ID](pb.Ptr(), selPasteboardItems)
	if array == 0 {
		return nil
	}
	count := objc.Send[uint](array, objcutil.SelCount)
	items := make([]*appkit.NSPasteboardItem, 0, count)
	for i := range count {
		id := objc.Send[objc.ID](array, objcutil.SelObjectAtIndex, i)
		items = append(items, appkit.NSPasteboardItemFromID(pureobjc.Retain(id)))
	}
	return items
}

func newPasteboardItem() *appkit.NSPasteboardItem {
	id := objcutil.AllocClass("NSPasteboardItem")
	id = objc.Send[objc.ID](id, objc.RegisterName("init"))
	return appkit.NSPasteboardItemFromID(id)
}

func filePathFromURL(urlStr string) string {
	if urlStr == "" {
		return ""
	}
	parsed, err := url.Parse(urlStr)
	if err != nil || parsed.Scheme != "file" {
		return ""
	}
	return parsed.Path
}
