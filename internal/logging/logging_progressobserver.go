// Port of tart's Logging/ProgressObserver.swift. Swift observes
// NSProgress.fractionCompleted via KVO; KVO observers cannot be registered
// through the purego bindings, so the observer polls instead, keeping the
// original behaviour of rendering at most once per second and skipping
// identical lines.
//go:build darwin

package logging

import (
	"fmt"
	"sync/atomic"
	"time"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// Progress abstracts Foundation's Progress for the observer.
type Progress interface {
	FractionCompleted() float64
	IsFinished() bool
}

// DownloadProgress is the Go counterpart of tart's locally created
// Progress(totalUnitCount:) used for download accounting.
type DownloadProgress struct {
	TotalUnitCount     int64
	completedUnitCount atomic.Int64
}

func NewDownloadProgress(totalUnitCount int64) *DownloadProgress {
	return &DownloadProgress{TotalUnitCount: totalUnitCount}
}

func (p *DownloadProgress) Add(n int64) { p.completedUnitCount.Add(n) }

func (p *DownloadProgress) FractionCompleted() float64 {
	if p.TotalUnitCount <= 0 {
		return 0
	}
	return float64(p.completedUnitCount.Load()) / float64(p.TotalUnitCount)
}

func (p *DownloadProgress) IsFinished() bool {
	return p.TotalUnitCount > 0 && p.completedUnitCount.Load() >= p.TotalUnitCount
}

// NSProgressWrapper adapts a Foundation NSProgress (e.g. from
// VZMacOSInstaller) to the Progress interface.
type NSProgressWrapper struct {
	Inner *foundation.NSProgress
}

func (p *NSProgressWrapper) FractionCompleted() float64 { return p.Inner.FractionCompleted() }
func (p *NSProgressWrapper) IsFinished() bool           { return p.Inner.IsFinished() }

// ProgressObserver ports tart's ProgressObserver class.
type ProgressObserver struct {
	progressToObserve Progress
	stop              chan struct{}
}

func NewProgressObserver(progress Progress) *ProgressObserver {
	return &ProgressObserver{progressToObserve: progress, stop: make(chan struct{})}
}

// Log starts rendering progress to renderer until the progress finishes or
// Stop is called.
func (o *ProgressObserver) Log(renderer Logger) {
	initialLine := progressLineToRender(o.progressToObserve)
	renderer.AppendNewLine(initialLine)

	go func() {
		lastRenderedLine := initialLine
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-o.stop:
				return
			case <-ticker.C:
			}

			line := progressLineToRender(o.progressToObserve)
			// Skip identical renders so non-interactive logs only see new
			// percent values.
			if line == lastRenderedLine {
				if o.progressToObserve.IsFinished() {
					return
				}
				continue
			}

			lastRenderedLine = line
			renderer.UpdateLastLine(line)

			if o.progressToObserve.IsFinished() {
				return
			}
		}
	}()
}

// Stop terminates the rendering goroutine.
func (o *ProgressObserver) Stop() {
	close(o.stop)
}

func progressLineToRender(progress Progress) string {
	return fmt.Sprintf("%d%%", int(100*progress.FractionCompleted()))
}
