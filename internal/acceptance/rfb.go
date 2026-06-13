// Minimal RFB 3.8 probe used by the VNC suite to verify that
// "run --vnc-experimental" serves a real framebuffer over the wire. It
// performs the VNC-auth handshake and captures one Raw frame, returning its
// dimensions — independently re-implementing the protocol so the acceptance
// suite exercises the server end to end rather than trusting weave's own
// client.
//go:build darwin

package main

import (
	"crypto/des"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// probeVNC connects to host:port, authenticates with password and captures a
// single framebuffer, returning its dimensions.
func probeVNC(host string, port int, password string) (width, height int, err error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Version handshake.
	version := make([]byte, 12)
	if _, err := io.ReadFull(conn, version); err != nil {
		return 0, 0, fmt.Errorf("read version: %w", err)
	}
	if string(version[:4]) != "RFB " {
		return 0, 0, fmt.Errorf("not an RFB server: %q", version)
	}
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return 0, 0, err
	}

	// Security negotiation.
	var typeCount uint8
	if err := binary.Read(conn, binary.BigEndian, &typeCount); err != nil {
		return 0, 0, err
	}
	if typeCount == 0 {
		return 0, 0, fmt.Errorf("server rejected the connection")
	}
	types := make([]byte, typeCount)
	if _, err := io.ReadFull(conn, types); err != nil {
		return 0, 0, err
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
		return 0, 0, fmt.Errorf("no supported security type offered: %v", types)
	}
	if _, err := conn.Write([]byte{chosen}); err != nil {
		return 0, 0, err
	}

	if chosen == 2 {
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(conn, challenge); err != nil {
			return 0, 0, err
		}
		response, err := vncAuthResponse(password, challenge)
		if err != nil {
			return 0, 0, err
		}
		if _, err := conn.Write(response); err != nil {
			return 0, 0, err
		}
	}

	var securityResult uint32
	if err := binary.Read(conn, binary.BigEndian, &securityResult); err != nil {
		return 0, 0, err
	}
	if securityResult != 0 {
		return 0, 0, fmt.Errorf("authentication failed")
	}

	// ClientInit (shared).
	if _, err := conn.Write([]byte{1}); err != nil {
		return 0, 0, err
	}

	// ServerInit.
	var serverInit struct {
		Width, Height uint16
		PixelFormat   [16]byte
		NameLength    uint32
	}
	if err := binary.Read(conn, binary.BigEndian, &serverInit); err != nil {
		return 0, 0, fmt.Errorf("read ServerInit: %w", err)
	}
	if _, err := io.CopyN(io.Discard, conn, int64(serverInit.NameLength)); err != nil {
		return 0, 0, err
	}
	width, height = int(serverInit.Width), int(serverInit.Height)

	// SetPixelFormat: 32bpp true colour.
	pixelFormat := []byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 0, 8, 16, 0, 0, 0}
	if _, err := conn.Write(pixelFormat); err != nil {
		return 0, 0, err
	}

	// SetEncodings: Raw + DesktopSize. _VZVNCServer drops clients that do
	// not advertise the DesktopSize (-223) pseudo-encoding, so it must be
	// included exactly as weave's own RFB client does.
	desktopSize := int32(-223)
	setEncodings := make([]byte, 4+8)
	setEncodings[0] = 2
	binary.BigEndian.PutUint16(setEncodings[2:], 2)
	binary.BigEndian.PutUint32(setEncodings[4:], 0)                   // Raw
	binary.BigEndian.PutUint32(setEncodings[8:], uint32(desktopSize)) // DesktopSize
	if _, err := conn.Write(setEncodings); err != nil {
		return 0, 0, err
	}

	// Request a full framebuffer update.
	request := make([]byte, 10)
	request[0] = 3
	binary.BigEndian.PutUint16(request[6:], uint16(width))
	binary.BigEndian.PutUint16(request[8:], uint16(height))
	if _, err := conn.Write(request); err != nil {
		return 0, 0, err
	}

	// Read messages until a FramebufferUpdate arrives, draining one Raw rect.
	for {
		var messageType uint8
		if err := binary.Read(conn, binary.BigEndian, &messageType); err != nil {
			return 0, 0, fmt.Errorf("read message type: %w", err)
		}
		switch messageType {
		case 0: // FramebufferUpdate
			header := make([]byte, 3)
			if _, err := io.ReadFull(conn, header); err != nil {
				return 0, 0, err
			}
			rectCount := int(binary.BigEndian.Uint16(header[1:3]))
			for i := 0; i < rectCount; i++ {
				var rect struct {
					X, Y, W, H uint16
					Encoding   int32
				}
				if err := binary.Read(conn, binary.BigEndian, &rect); err != nil {
					return 0, 0, err
				}
				switch rect.Encoding {
				case 0: // Raw
					pixels := int64(rect.W) * int64(rect.H) * 4
					if _, err := io.CopyN(io.Discard, conn, pixels); err != nil {
						return 0, 0, err
					}
				case -223: // DesktopSize: no pixel data, just the new size
					width, height = int(rect.W), int(rect.H)
				default:
					return 0, 0, fmt.Errorf("unexpected encoding %d (wanted Raw or DesktopSize)", rect.Encoding)
				}
			}
			return width, height, nil
		case 1: // SetColourMapEntries
			head := make([]byte, 5)
			if _, err := io.ReadFull(conn, head); err != nil {
				return 0, 0, err
			}
			colours := int64(binary.BigEndian.Uint16(head[3:5]))
			if _, err := io.CopyN(io.Discard, conn, colours*6); err != nil {
				return 0, 0, err
			}
		case 2: // Bell
		case 3: // ServerCutText
			head := make([]byte, 7)
			if _, err := io.ReadFull(conn, head); err != nil {
				return 0, 0, err
			}
			if _, err := io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint32(head[3:7]))); err != nil {
				return 0, 0, err
			}
		default:
			return 0, 0, fmt.Errorf("unexpected server message %d", messageType)
		}
	}
}

func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	copy(key, password)
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
	for i := range 8 {
		result = result<<1 | (b>>i)&1
	}
	return result
}
