// Port of tart's Diskutil.swift: shells out to diskutil(8) via NSTask and
// parses the --plist output with NSPropertyListSerialization (Swift:
// Process + PropertyListDecoder).
//go:build darwin

package diskimage

import (
	"fmt"
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	"github.com/ebitengine/purego/objc"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
)

// SizeInfo ports Diskutil.swift's SizeInfo ("Size Info" plist dictionary).
type SizeInfo struct {
	TotalBytes *uint64 // "Total Bytes"
}

// ImageInfo ports Diskutil.swift's ImageInfo ("diskutil image info" output).
type ImageInfo struct {
	SizeInfo *SizeInfo // "Size Info"
	Size     *uint64   // "Size"
}

// TotalBytes ports ImageInfo.totalBytes().
func (i *ImageInfo) TotalBytes() (int, error) {
	if i.SizeInfo != nil && i.SizeInfo.TotalBytes != nil {
		return int(*i.SizeInfo.TotalBytes), nil
	}
	if i.Size != nil {
		return int(*i.Size), nil
	}
	return 0, weaveerrors.ErrGeneric("Could not find size information in disk image info")
}

// DiskutilImageCreate ports Diskutil.imageCreate(diskURL:sizeGB:): creates a
// blank ASIF disk image.
func DiskutilImageCreate(diskURL *foundation.NSURL, sizeGB uint16) error {
	_, _, err := DiskutilRun([]string{
		"image", "create", "blank",
		"--format", "ASIF",
		"--size", fmt.Sprintf("%dG", sizeGB),
		"--volumeName", "Weave",
		objcutil.GoStr(diskURL.Path()),
	})
	if err != nil {
		return weaveerrors.ErrFailedToCreateDisk("Failed to create ASIF disk image: %v", err)
	}
	return nil
}

// DiskutilImageInfo ports Diskutil.imageInfo(_:).
func DiskutilImageInfo(diskURL *foundation.NSURL) (*ImageInfo, error) {
	stdoutData, _, err := DiskutilRun([]string{
		"image", "info", "--plist",
		objcutil.GoStr(diskURL.Path()),
	})
	if err != nil {
		return nil, err
	}

	plistID, err := foundation.NSPropertyListSerializationPropertyListWithDataOptionsFormatError(
		objcutil.BytesToNSData(stdoutData), 0, nil)
	if err != nil || plistID == 0 {
		return nil, weaveerrors.ErrGeneric("Failed to parse \"diskutil image info --plist\" output: %v", err)
	}

	info := &ImageInfo{}
	if sizeID := objc.Send[objc.ID](plistID, objcutil.SelObjectForKey, pureobjc.NSString("Size")); sizeID != 0 {
		size := foundation.NSNumberFromID(pureobjc.Retain(sizeID)).UnsignedLongLongValue()
		info.Size = &size
	}
	if sizeInfoID := objc.Send[objc.ID](plistID, objcutil.SelObjectForKey, pureobjc.NSString("Size Info")); sizeInfoID != 0 {
		info.SizeInfo = &SizeInfo{}
		if totalID := objc.Send[objc.ID](sizeInfoID, objcutil.SelObjectForKey, pureobjc.NSString("Total Bytes")); totalID != 0 {
			totalBytes := foundation.NSNumberFromID(pureobjc.Retain(totalID)).UnsignedLongLongValue()
			info.SizeInfo.TotalBytes = &totalBytes
		}
	}

	return info, nil
}

// DiskutilRun ports Diskutil.run(_:): executes diskutil with the given
// arguments and returns (stdout, stderr).
func DiskutilRun(arguments []string) ([]byte, []byte, error) {
	diskutilURL := objcutil.ResolveBinaryPath("diskutil")
	if diskutilURL == nil {
		return nil, nil, weaveerrors.ErrGeneric("\"diskutil\" binary is not found in PATH")
	}

	task := foundation.NSTaskFromID(objc.Send[objc.ID](objc.ID(objc.GetClass("NSTask")), objc.RegisterName("new")))
	task.SetExecutableURL(diskutilURL)
	task.SetArguments(objcutil.NSStringArray(arguments))

	stdoutPipe := foundation.NSPipePipe()
	task.SetStandardOutput(stdoutPipe.Ptr())
	stderrPipe := foundation.NSPipePipe()
	task.SetStandardError(stderrPipe.Ptr())

	if _, err := task.LaunchAndReturnError(); err != nil {
		return nil, nil, weaveerrors.ErrGeneric("\"%s\" failed: %v", strings.Join(arguments, " "), err)
	}
	task.WaitUntilExit()

	stdoutNSData, err := stdoutPipe.FileHandleForReading().ReadDataToEndOfFileAndReturnError()
	if err != nil {
		return nil, nil, weaveerrors.ErrGeneric("\"%s\" failed: %v", strings.Join(arguments, " "), err)
	}
	stderrNSData, err := stderrPipe.FileHandleForReading().ReadDataToEndOfFileAndReturnError()
	if err != nil {
		return nil, nil, weaveerrors.ErrGeneric("\"%s\" failed: %v", strings.Join(arguments, " "), err)
	}

	stdoutData := objcutil.NSDataToBytes(stdoutNSData)
	stderrData := objcutil.NSDataToBytes(stderrNSData)

	if status := task.TerminationStatus(); status != 0 {
		return nil, nil, weaveerrors.ErrGeneric("\"%s\" failed with exit code %d: %s",
			strings.Join(arguments, " "), status, FirstNonEmptyLine(string(stderrData), string(stdoutData)))
	}

	return stdoutData, stderrData, nil
}

// FirstNonEmptyLine ports Diskutil.FirstNonEmptyLine(_:).
func FirstNonEmptyLine(outputs ...string) string {
	for _, output := range outputs {
		for line := range strings.SplitSeq(output, "\n") {
			if line != "" {
				return line
			}
		}
	}
	return ""
}
