// Port of tart's Fetcher.swift. The Swift original streams response chunks
// through a URLSessionDataDelegate; ObjC delegate classes cannot be defined
// through the purego bindings, so this port always uses a download task —
// which spools the body to a temporary file, exactly like Fetcher's
// viaFile mode — and then streams that file in 16 MiB chunks.
//go:build darwin

package fetcher

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/ebitengine/purego/objc"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// fetcherBufferFlushSize mirrors the Delegate's 16 MiB buffer flush size.
const fetcherBufferFlushSize = 16 * 1024 * 1024

// fetcherURLSession mirrors Fetcher.swift's file-private urlSession: a
// shared session that never carries cookies between requests, because Harbor
// expects a CSRF token to be present whenever the HTTP client carries a
// session cookie and fails otherwise (cirruslabs/tart#295).
var fetcherURLSession = sync.OnceValue(func() *foundation.NSURLSession {
	config := foundation.NSURLSessionConfigurationDefaultSessionConfiguration()
	config.SetHTTPShouldSetCookies(false)
	return foundation.NSURLSessionSessionWithConfiguration(config)
})

// FetchChunk is one element of the byte stream returned by FetcherFetch
// (Swift: AsyncThrowingStream<Data, Error>).
type FetchChunk struct {
	Data []byte
	Err  error
}

// FetcherFetch ports Fetcher.fetch(_:viaFile:). The chunk channel is closed
// after the final chunk; a chunk with Err set terminates the stream early.
// Unlike the Swift original, the response is returned only after the body
// has been spooled to disk, so the viaFile parameter is accepted for parity
// but has no effect.
func FetcherFetch(ctx context.Context, request *foundation.NSURLRequest, viaFile bool) (<-chan FetchChunk, *foundation.NSHTTPURLResponse, error) {
	_ = viaFile

	type downloadResult struct {
		spoolPath string
		response  *foundation.NSHTTPURLResponse
		err       error
	}
	resultCh := make(chan downloadResult, 1)

	// The generated DownloadTaskWithRequestCompletionHandler cannot be used:
	// purego's objc.NewBlock requires the Go function to take the Block as
	// its first parameter, which the generated bindings omit. Build the
	// block and send the message directly instead.
	completionBlock := objc.NewBlock(func(_ objc.Block, locationID objc.ID, responseID objc.ID, errID objc.ID) {
		if errID != 0 {
			resultCh <- downloadResult{err: pureobjc.NSErrorToError(errID)}
			return
		}
		locationURL := foundation.NSURLFromID(pureobjc.Retain(locationID))
		httpResponse := foundation.NSHTTPURLResponseFromID(pureobjc.Retain(responseID))

		// The download's temporary file is deleted as soon as this handler
		// returns, so move it aside first.
		spoolPath := filepath.Join(os.TempDir(), "weave-fetch-"+filepath.Base(objcutil.GoStr(locationURL.Path())))
		_, err := foundation.NSFileManagerDefaultManager().MoveItemAtURLToURLError(
			locationURL, foundation.NSURLFileURLWithPath(objcutil.NSStr(spoolPath)))
		if err != nil {
			resultCh <- downloadResult{err: err}
			return
		}

		resultCh <- downloadResult{spoolPath: spoolPath, response: httpResponse}
	})

	taskID := objc.Send[objc.ID](fetcherURLSession().Ptr(),
		objc.RegisterName("downloadTaskWithRequest:completionHandler:"), request.Ptr(), completionBlock)
	task := foundation.NSURLSessionDownloadTaskFromID(pureobjc.Retain(taskID))
	task.Resume()

	var result downloadResult
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		task.Cancel()
		return nil, nil, ctx.Err()
	}
	if result.err != nil {
		return nil, nil, result.err
	}

	chunks := make(chan FetchChunk)
	go func() {
		defer close(chunks)
		defer os.Remove(result.spoolPath)

		spoolFile, err := os.Open(result.spoolPath)
		if err != nil {
			chunks <- FetchChunk{Err: err}
			return
		}
		defer spoolFile.Close()

		buffer := make([]byte, fetcherBufferFlushSize)
		for {
			n, err := spoolFile.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				select {
				case chunks <- FetchChunk{Data: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				chunks <- FetchChunk{Err: err}
				return
			}
		}
	}()

	return chunks, result.response, nil
}
