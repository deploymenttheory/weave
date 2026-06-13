//go:build darwin

package screenviewer

import (
	"bufio"
	"image"
	"image/color"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestScreenServer(t *testing.T) {
	server, err := NewScreenServer()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// Push a frame.
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	server.Push(img)

	// The index page renders an <img> pointing at the stream.
	response, err := http.Get(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 1024)
	n, _ := response.Body.Read(body)
	response.Body.Close()
	if !strings.Contains(string(body[:n]), `src="/stream"`) {
		t.Errorf("index page missing the stream image:\n%s", body[:n])
	}

	// The MJPEG stream should deliver at least one JPEG part.
	client := &http.Client{Timeout: 3 * time.Second}
	streamResp, err := client.Get(server.URL() + "stream")
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	if ct := streamResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/x-mixed-replace") {
		t.Fatalf("Content-Type = %q, want multipart/x-mixed-replace", ct)
	}
	reader := bufio.NewReader(streamResp.Body)
	sawBoundary, sawJPEG := false, false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "--frame") {
			sawBoundary = true
		}
		if strings.Contains(line, "image/jpeg") {
			sawJPEG = true
		}
		if sawBoundary && sawJPEG {
			break
		}
	}
	if !sawBoundary || !sawJPEG {
		t.Errorf("MJPEG stream did not deliver a JPEG frame (boundary=%v jpeg=%v)", sawBoundary, sawJPEG)
	}
}
