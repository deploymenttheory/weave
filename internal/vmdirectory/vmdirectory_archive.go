// Port of tart's VMDirectory+Archive.swift: export/import of a VM directory
// using Apple's archive format with LZFSE compression. The AppleArchive
// library binding exposes the low-level AA byte streams but not the
// directory-content encode helpers the Swift API uses, so this drives
// /usr/bin/aa, which produces and consumes the identical .aar format.
//go:build darwin

package vmdirectory

import (
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/ebitengine/purego/objc"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// ExportToArchive ports VMDirectory.exportToArchive(path:).
func (d *VMDirectory) ExportToArchive(path string) error {
	if err := runAA([]string{
		"archive",
		"-d", objcutil.GoStr(d.BaseURL.Path()),
		"-o", path,
		"-a", "lzfse",
	}); err != nil {
		return weaveerrors.ErrExportFailed(err.Error())
	}
	return nil
}

// ImportFromArchive ports VMDirectory.importFromArchive(path:).
func (d *VMDirectory) ImportFromArchive(path string) error {
	if err := runAA([]string{
		"extract",
		"-d", objcutil.GoStr(d.BaseURL.Path()),
		"-i", path,
	}); err != nil {
		return weaveerrors.ErrImportFailed(err.Error())
	}
	return nil
}

func runAA(arguments []string) error {
	task := foundation.NSTaskFromID(objc.Send[objc.ID](objc.ID(objc.GetClass("NSTask")), objc.RegisterName("new")))
	task.SetExecutableURL(objcutil.NSURLFromPath("/usr/bin/aa"))
	task.SetArguments(objcutil.NSStringArray(arguments))

	stderrPipe := foundation.NSPipePipe()
	task.SetStandardError(stderrPipe.Ptr())

	if _, err := task.LaunchAndReturnError(); err != nil {
		return err
	}
	task.WaitUntilExit()

	if status := task.TerminationStatus(); status != 0 {
		stderrData, _ := stderrPipe.FileHandleForReading().ReadDataToEndOfFileAndReturnError()
		return weaveerrors.ErrGeneric("aa failed with exit code %d: %s", status,
			diskimage.FirstNonEmptyLine(string(objcutil.NSDataToBytes(stderrData))))
	}
	return nil
}
