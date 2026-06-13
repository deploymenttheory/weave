// Port of tart's Logging/URLSessionLogger.swift. The Swift class is a
// URLSessionTaskDelegate rendering upload progress; delegates cannot be
// registered through the purego bindings (and uploads here go through
// FetcherFetch), so this renders from a DownloadProgress instead.
//go:build darwin

package logging

import "fmt"

// URLSessionLogger ports tart's URLSessionLogger class.
type URLSessionLogger struct {
	renderer Logger
}

func NewURLSessionLogger(renderer Logger) *URLSessionLogger {
	return &URLSessionLogger{renderer: renderer}
}

// DidSendBodyData mirrors urlSession(_:task:didSendBodyData:…).
func (l *URLSessionLogger) DidSendBodyData(totalBytesSent int64, totalBytesExpectedToSend int64) {
	if totalBytesExpectedToSend <= 0 {
		return
	}
	l.renderer.UpdateLastLine(fmt.Sprintf("%d%%", 100*totalBytesSent/totalBytesExpectedToSend))
}
