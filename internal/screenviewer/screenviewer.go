// A view-only screen viewer for "setup --show-screen": a tiny local HTTP
// server that streams the framebuffers the automation captures as MJPEG, so
// an operator can watch the Setup Assistant automation in a browser without
// being able to send any input into the guest (unlike a native VM window,
// which forwards mouse/keyboard and would fight the automation).
//go:build darwin

package screenviewer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"time"

	weavevnc "github.com/deploymenttheory/weave/internal/vnc"
)

// ScreenServer holds the latest captured frame and streams it as MJPEG.
type ScreenServer struct {
	listener net.Listener
	server   *http.Server

	mu     sync.Mutex
	latest []byte // latest JPEG-encoded frame
}

// NewScreenServer starts a viewer on a random loopback port.
func NewScreenServer() (*ScreenServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	screen := &ScreenServer{listener: listener}

	mux := http.NewServeMux()
	mux.HandleFunc("/", screen.handleIndex)
	mux.HandleFunc("/stream", screen.handleStream)
	screen.server = &http.Server{Handler: mux}

	go func() { _ = screen.server.Serve(listener) }()
	return screen, nil
}

// URL is the page an operator opens to watch.
func (s *ScreenServer) URL() string {
	return "http://" + s.listener.Addr().String() + "/"
}

// OpenInBrowser best-effort opens url in the default browser.
func OpenInBrowser(url string) {
	_ = exec.Command("open", url).Start()
}

// StreamVNCToViewer continuously captures from a VNC server with a single
// dedicated client and pushes frames to the viewer until ctx is cancelled.
// This is the "operational" wiring (e.g. "run --show-screen"), where no
// automation owns the VNC connection — _VZVNCServer only supports one client
// at a time, so the viewer either owns the sole client (here) or shares the
// automation's captures (the provisioning wiring).
func StreamVNCToViewer(ctx context.Context, host string, port int, password string, server *ScreenServer) {
	const frameInterval = 200 * time.Millisecond // ~5 fps
	for ctx.Err() == nil {
		client, err := weavevnc.DialVNC(ctx, host, port, password)
		if err != nil {
			if sleepCtx(ctx, time.Second) {
				return
			}
			continue
		}
		for ctx.Err() == nil {
			img, err := client.CaptureFramebuffer(ctx)
			if err != nil {
				break // drop and reconnect
			}
			server.Push(img)
			if sleepCtx(ctx, frameInterval) {
				break
			}
		}
		client.Close()
	}
}

// sleepCtx sleeps for d, returning true if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

// Close stops the viewer.
func (s *ScreenServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
}

// Push stores a new frame (JPEG-encoded) for the stream. Safe to call from
// the automation after each framebuffer capture.
func (s *ScreenServer) Push(img image.Image) {
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 70}); err != nil {
		return
	}
	s.mu.Lock()
	s.latest = buffer.Bytes()
	s.mu.Unlock()
}

func (s *ScreenServer) latestFrame() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

func (s *ScreenServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><title>weave setup — view only</title>
<style>html,body{margin:0;height:100%;background:#111;display:flex;align-items:center;justify-content:center}
img{max-width:100%;max-height:100%}</style></head>
<body><img src="/stream" alt="VM screen (view only)"></body></html>`)
}

// handleStream serves multipart/x-mixed-replace MJPEG. A 4 fps tick is plenty
// to watch Setup Assistant steps and keeps the connection cheap.
func (s *ScreenServer) handleStream(w http.ResponseWriter, r *http.Request) {
	const boundary = "frame"
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			frame := s.latestFrame()
			if frame == nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
