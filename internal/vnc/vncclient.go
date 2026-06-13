// Minimal RFB 3.8 (VNC) client used by unattended setup automation to
// capture the framebuffer and inject input into a VM served by the private
// _VZVNCServer (run --vnc-experimental). Hand-rolled to avoid a dependency:
// only the Raw encoding and VNC authentication are implemented. The
// pixel format is normalized to 32bpp little-endian RGBX so the framebuffer
// maps directly onto image.RGBA.
//go:build darwin

package vnc

import (
	"context"
	"crypto/des"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"net"
	"sync"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

const (
	rfbEncodingRaw               = 0
	rfbEncodingDesktopSize int32 = -223
	rfbEncodingLastRect    int32 = -224
	rfbEncodingCursor      int32 = -239

	rfbMessageFramebufferUpdate   = 0
	rfbMessageSetColourMapEntries = 1
	rfbMessageBell                = 2
	rfbMessageServerCutText       = 3
)

// VNCClient is a connected RFB session.
type VNCClient struct {
	conn   net.Conn
	Width  int
	Height int
	fb     []byte // RGBA, Width*Height*4

	// Dial parameters, kept so Redial can re-establish a wedged session.
	host     string
	port     int
	password string

	// mu serialises whole VNC operations (a capture's request+response, or an
	// input event's write) so a background viewer-capture loop can share the
	// single connection with the automation without interleaving messages.
	mu sync.Mutex
}

// DialVNC connects, authenticates (VNC auth when password is non-empty and
// offered) and completes the RFB handshake.
func DialVNC(ctx context.Context, host string, port int, password string) (*VNCClient, error) {
	client := &VNCClient{host: host, port: port, password: password}
	if err := client.dialLocked(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// dialLocked establishes (or re-establishes) the TCP connection and completes
// the RFB handshake, replacing conn/Width/Height/fb in place. The caller must
// hold c.mu (or, for DialVNC, own the client exclusively).
func (c *VNCClient) dialLocked(ctx context.Context) error {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(c.host, fmt.Sprintf("%d", c.port)))
	if err != nil {
		return weaveerrors.ErrVNCAutomationFailed(fmt.Sprintf("connect: %v", err))
	}
	c.conn = conn
	if err := c.handshake(c.password); err != nil {
		_ = conn.Close()
		return err
	}
	return nil
}

// Redial drops the current connection and establishes a fresh RFB session to
// the same endpoint. _VZVNCServer sessions can wedge — the server stops
// answering FramebufferUpdateRequests on a long-lived connection while a new
// connection works fine — and it permits only one client at a time, so the
// old connection is closed before dialing.
func (c *VNCClient) Redial(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.Close()
	return c.dialLocked(ctx)
}

func (c *VNCClient) Close() error { return c.conn.Close() }

func (c *VNCClient) handshake(password string) error {
	// Version exchange.
	version := make([]byte, 12)
	if _, err := io.ReadFull(c.conn, version); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(fmt.Sprintf("reading server version: %v", err))
	}
	if string(version[:4]) != "RFB " {
		return weaveerrors.ErrVNCAutomationFailed(fmt.Sprintf("not an RFB server: %q", version))
	}
	if _, err := c.conn.Write([]byte("RFB 003.008\n")); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}

	// Security negotiation.
	var typeCount uint8
	if err := binary.Read(c.conn, binary.BigEndian, &typeCount); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}
	if typeCount == 0 {
		return weaveerrors.ErrVNCAutomationFailed("server rejected connection: " + c.readReasonString())
	}
	types := make([]byte, typeCount)
	if _, err := io.ReadFull(c.conn, types); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}

	var chosen byte
	for _, securityType := range types {
		if securityType == 2 && password != "" {
			chosen = 2
			break
		}
		if securityType == 1 && chosen == 0 {
			chosen = 1
		}
	}
	if chosen == 0 {
		return weaveerrors.ErrVNCAutomationFailed(fmt.Sprintf("no supported security type offered (got %v)", types))
	}
	if _, err := c.conn.Write([]byte{chosen}); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}

	if chosen == 2 {
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(c.conn, challenge); err != nil {
			return weaveerrors.ErrVNCAutomationFailed(err.Error())
		}
		response, err := vncEncryptChallenge(password, challenge)
		if err != nil {
			return weaveerrors.ErrVNCAutomationFailed(err.Error())
		}
		if _, err := c.conn.Write(response); err != nil {
			return weaveerrors.ErrVNCAutomationFailed(err.Error())
		}
	}

	// SecurityResult (sent for both None and VNC auth in 3.8).
	var result uint32
	if err := binary.Read(c.conn, binary.BigEndian, &result); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}
	if result != 0 {
		return weaveerrors.ErrVNCAutomationFailed("VNC authentication failed: " + c.readReasonString())
	}

	// ClientInit: shared session.
	if _, err := c.conn.Write([]byte{1}); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}

	// ServerInit.
	var serverInit struct {
		Width, Height uint16
		PixelFormat   [16]byte
		NameLength    uint32
	}
	if err := binary.Read(c.conn, binary.BigEndian, &serverInit); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(fmt.Sprintf("reading ServerInit: %v", err))
	}
	name := make([]byte, serverInit.NameLength)
	if _, err := io.ReadFull(c.conn, name); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}
	c.Width, c.Height = int(serverInit.Width), int(serverInit.Height)
	c.fb = make([]byte, c.Width*c.Height*4)

	// SetPixelFormat: 32bpp little-endian true colour, R at byte 0, G at
	// byte 1, B at byte 2 — i.e. memory order RGBX.
	pixelFormat := []byte{
		0, 0, 0, 0, // message type 0 + 3 bytes padding
		32,     // bits per pixel
		24,     // depth
		0,      // big-endian flag
		1,      // true colour
		0, 255, // red max
		0, 255, // green max
		0, 255, // blue max
		0,       // red shift
		8,       // green shift
		16,      // blue shift
		0, 0, 0, // padding
	}
	if _, err := c.conn.Write(pixelFormat); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}

	// SetEncodings: Raw plus the pseudo-encodings Apple's OSXvnc-derived
	// _VZVNCServer expects (lume parity). Omitting a pseudo-encoding the
	// server wants to send wedges the session: without DesktopSize it drops
	// the client outright, and without Cursor it stops answering update
	// requests after the first one whenever the guest cursor changes (first
	// boot spinners), which starved the unattended automation of frames.
	supported := []int32{rfbEncodingRaw, rfbEncodingDesktopSize, rfbEncodingCursor, rfbEncodingLastRect}
	encodings := make([]byte, 4+4*len(supported))
	encodings[0] = 2 // message type
	binary.BigEndian.PutUint16(encodings[2:], uint16(len(supported)))
	for i, encoding := range supported {
		binary.BigEndian.PutUint32(encodings[4+4*i:], uint32(encoding)) // two's complement on the wire
	}
	if _, err := c.conn.Write(encodings); err != nil {
		return weaveerrors.ErrVNCAutomationFailed(err.Error())
	}

	return nil
}

func (c *VNCClient) readReasonString() string {
	var length uint32
	if err := binary.Read(c.conn, binary.BigEndian, &length); err != nil || length > 4096 {
		return "unknown reason"
	}
	reason := make([]byte, length)
	if _, err := io.ReadFull(c.conn, reason); err != nil {
		return "unknown reason"
	}
	return string(reason)
}

// vncEncryptChallenge implements the VNC authentication scheme: the 16-byte
// challenge is DES-ECB encrypted with the password as key — with each key
// byte bit-reversed (the classic VNC quirk).
func vncEncryptChallenge(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	copy(key, password) // truncate/zero-pad to 8 bytes
	for i := range key {
		key[i] = reverseBits(key[i])
	}

	cipher, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	response := make([]byte, 16)
	cipher.Encrypt(response[0:8], challenge[0:8])
	cipher.Encrypt(response[8:16], challenge[8:16])
	return response, nil
}

func reverseBits(b byte) byte {
	var result byte
	for i := 0; i < 8; i++ {
		result = result<<1 | (b>>i)&1
	}
	return result
}

// CaptureFramebuffer requests a full (non-incremental) update and returns
// the framebuffer as an RGBA image.
func (c *VNCClient) CaptureFramebuffer(ctx context.Context) (*image.RGBA, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	request := make([]byte, 10)
	request[0] = 3 // FramebufferUpdateRequest
	request[1] = 0 // non-incremental
	binary.BigEndian.PutUint16(request[6:], uint16(c.Width))
	binary.BigEndian.PutUint16(request[8:], uint16(c.Height))
	if _, err := c.writeLocked(request); err != nil {
		return nil, weaveerrors.ErrFramebufferCaptureFailed(err.Error())
	}

	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	for first := true; ; first = false {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// A fresh per-message deadline: each server message (including a
		// multi-megabyte full-frame Raw rect) gets a generous window, so a
		// slow-but-progressing transfer or a brief server stall during a
		// screen transition does not abort the whole capture. The first
		// message gets a shorter window: a healthy server answers a
		// non-incremental request immediately, so silence here means the
		// session has wedged and the caller should Redial.
		deadline := 30 * time.Second
		if first {
			deadline = 10 * time.Second
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(deadline))

		var messageType uint8
		if err := binary.Read(c.conn, binary.BigEndian, &messageType); err != nil {
			return nil, weaveerrors.ErrFramebufferCaptureFailed(err.Error())
		}

		switch messageType {
		case rfbMessageFramebufferUpdate:
			if err := c.readFramebufferUpdate(); err != nil {
				return nil, err
			}
			img := image.NewRGBA(image.Rect(0, 0, c.Width, c.Height))
			copy(img.Pix, c.fb)
			// The X (4th) byte arrives undefined; force full alpha.
			for i := 3; i < len(img.Pix); i += 4 {
				img.Pix[i] = 0xFF
			}
			return img, nil

		case rfbMessageSetColourMapEntries:
			header := make([]byte, 5)
			if _, err := io.ReadFull(c.conn, header); err != nil {
				return nil, weaveerrors.ErrFramebufferCaptureFailed(err.Error())
			}
			colours := int(binary.BigEndian.Uint16(header[3:5]))
			if _, err := io.CopyN(io.Discard, c.conn, int64(colours*6)); err != nil {
				return nil, weaveerrors.ErrFramebufferCaptureFailed(err.Error())
			}

		case rfbMessageBell:
			// No payload.

		case rfbMessageServerCutText:
			header := make([]byte, 7)
			if _, err := io.ReadFull(c.conn, header); err != nil {
				return nil, weaveerrors.ErrFramebufferCaptureFailed(err.Error())
			}
			length := int64(binary.BigEndian.Uint32(header[3:7]))
			if _, err := io.CopyN(io.Discard, c.conn, length); err != nil {
				return nil, weaveerrors.ErrFramebufferCaptureFailed(err.Error())
			}

		default:
			return nil, weaveerrors.ErrFramebufferCaptureFailed(fmt.Sprintf("unexpected server message type %d", messageType))
		}
	}
}

func (c *VNCClient) readFramebufferUpdate() error {
	header := make([]byte, 3)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return weaveerrors.ErrFramebufferCaptureFailed(err.Error())
	}
	rectCount := int(binary.BigEndian.Uint16(header[1:3]))

	for i := 0; i < rectCount; i++ {
		var rect struct {
			X, Y, W, H uint16
			Encoding   int32
		}
		if err := binary.Read(c.conn, binary.BigEndian, &rect); err != nil {
			return weaveerrors.ErrFramebufferCaptureFailed(err.Error())
		}

		switch rect.Encoding {
		case rfbEncodingRaw:
			rowBytes := int(rect.W) * 4
			row := make([]byte, rowBytes)
			for y := 0; y < int(rect.H); y++ {
				if _, err := io.ReadFull(c.conn, row); err != nil {
					return weaveerrors.ErrFramebufferCaptureFailed(err.Error())
				}
				destY := int(rect.Y) + y
				if destY >= c.Height {
					continue
				}
				dest := (destY*c.Width + int(rect.X)) * 4
				copy(c.fb[dest:dest+min(rowBytes, len(c.fb)-dest)], row)
			}

		case rfbEncodingDesktopSize:
			c.Width, c.Height = int(rect.W), int(rect.H)
			c.fb = make([]byte, c.Width*c.Height*4)

		case rfbEncodingCursor:
			// Cursor shape update: w*h pixels + a 1bpp bitmask, both ignored
			// (the automation does not render a client-side cursor).
			pixels := int64(rect.W) * int64(rect.H) * 4
			bitmask := int64((int(rect.W)+7)/8) * int64(rect.H)
			if _, err := io.CopyN(io.Discard, c.conn, pixels+bitmask); err != nil {
				return weaveerrors.ErrFramebufferCaptureFailed(err.Error())
			}

		case rfbEncodingLastRect:
			// No payload; signals the end of this update's rectangles.
			return nil

		default:
			return weaveerrors.ErrFramebufferCaptureFailed(fmt.Sprintf("unsupported encoding %d", rect.Encoding))
		}
	}
	return nil
}

// PointerEvent sends a pointer position/button update. buttons is the RFB
// button mask (bit 0 = left).
// pointerEventLocked writes one pointer event on the wire. The caller MUST
// already hold c.mu — gesture helpers (clickLocked) use this to keep a whole
// press/release atomic.
func (c *VNCClient) pointerEventLocked(x int, y int, buttons uint8) error {
	message := make([]byte, 6)
	message[0] = 5
	message[1] = buttons
	binary.BigEndian.PutUint16(message[2:], uint16(max(0, x)))
	binary.BigEndian.PutUint16(message[4:], uint16(max(0, y)))
	if _, err := c.writeLocked(message); err != nil {
		return weaveerrors.ErrInputSimulationFailed(err.Error())
	}
	return nil
}

// writeLocked writes one client message with a bounded deadline so input on a
// dead connection errors instead of blocking forever once the TCP send buffer
// fills. Caller must hold c.mu.
func (c *VNCClient) writeLocked(message []byte) (int, error) {
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()
	return c.conn.Write(message)
}

func (c *VNCClient) PointerEvent(x int, y int, buttons uint8) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pointerEventLocked(x, y, buttons)
}

// keyEventLocked writes one key event on the wire; caller MUST hold c.mu.
func (c *VNCClient) keyEventLocked(keysym uint32, down bool) error {
	message := make([]byte, 8)
	message[0] = 4
	if down {
		message[1] = 1
	}
	binary.BigEndian.PutUint32(message[4:], keysym)
	if _, err := c.writeLocked(message); err != nil {
		return weaveerrors.ErrInputSimulationFailed(err.Error())
	}
	return nil
}

// KeyEvent sends an X11 keysym press or release.
func (c *VNCClient) KeyEvent(keysym uint32, down bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyEventLocked(keysym, down)
}

// clickLocked performs a full move→press→release gesture; caller MUST hold
// c.mu. Holding the lock across the whole gesture is essential: in
// --show-screen mode the viewer's capture loop shares this single VNC
// connection (the _VZVNCServer permits only one client), and a frame capture
// slipping between button-down and button-up would stretch the click into a
// press-and-hold the guest never registers as a click — the exact failure that
// stalled setup on the Language screen's arrow.
func (c *VNCClient) clickLocked(x int, y int) error {
	if err := c.pointerEventLocked(x, y, 0); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	if err := c.pointerEventLocked(x, y, 1); err != nil {
		return err
	}
	time.Sleep(80 * time.Millisecond)
	return c.pointerEventLocked(x, y, 0)
}

// Click moves to (x, y) and performs a left click.
func (c *VNCClient) Click(x int, y int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clickLocked(x, y)
}

// DoubleClick performs two quick left clicks at (x, y).
func (c *VNCClient) DoubleClick(x int, y int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.clickLocked(x, y); err != nil {
		return err
	}
	time.Sleep(80 * time.Millisecond)
	return c.clickLocked(x, y)
}

// pressKeysymLocked taps one keysym; caller MUST hold c.mu.
func (c *VNCClient) pressKeysymLocked(keysym uint32) error {
	if err := c.keyEventLocked(keysym, true); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	return c.keyEventLocked(keysym, false)
}

// PressKeysym taps one keysym.
func (c *VNCClient) PressKeysym(keysym uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pressKeysymLocked(keysym)
}

// TypeText types a string character by character with delayMS between
// characters (0 means a small default).
func (c *VNCClient) TypeText(text string, delayMS int) error {
	delay := time.Duration(delayMS) * time.Millisecond
	if delayMS == 0 {
		delay = 50 * time.Millisecond
	}
	for _, character := range text {
		keysym, needShift, ok := KeysymForRune(character)
		if !ok {
			continue
		}
		// Keep each character's optional shift + tap atomic against the
		// shared-connection capture loop (see clickLocked).
		if err := c.typeRune(keysym, needShift); err != nil {
			return err
		}
		time.Sleep(delay)
	}
	return nil
}

// typeRune emits one character (with optional Shift) as a single locked gesture.
func (c *VNCClient) typeRune(keysym uint32, needShift bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if needShift {
		if err := c.keyEventLocked(KeysymShiftL, true); err != nil {
			return err
		}
	}
	if err := c.pressKeysymLocked(keysym); err != nil {
		return err
	}
	if needShift {
		return c.keyEventLocked(KeysymShiftL, false)
	}
	return nil
}

// Hotkey presses every modifier and then the main key with no delay between
// them (a simultaneous press), holds the combination, then releases all keys
// in reverse order. The 300ms hold matches lume and gives the guest time to
// register the chord (Cmd+Space etc.).
func (c *VNCClient) Hotkey(modifiers []uint32, keysym uint32) error {
	// Atomic against the shared-connection capture loop (see clickLocked): a
	// frame read slipping into the chord could drop a modifier and turn
	// Cmd+Space into a bare Space.
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, modifier := range modifiers {
		if err := c.keyEventLocked(modifier, true); err != nil {
			return err
		}
	}
	if err := c.keyEventLocked(keysym, true); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	if err := c.keyEventLocked(keysym, false); err != nil {
		return err
	}
	for i := len(modifiers) - 1; i >= 0; i-- {
		if err := c.keyEventLocked(modifiers[i], false); err != nil {
			return err
		}
	}
	time.Sleep(50 * time.Millisecond)
	return nil
}
