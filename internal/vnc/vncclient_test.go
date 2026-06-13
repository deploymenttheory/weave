//go:build darwin

package vnc

import (
	"bytes"
	"crypto/des"
	"testing"
)

// TestVNCChallengeEncryption checks the bit-reversal quirk and that the
// encryption round-trips with a DES decryption under the same reversed key.
func TestVNCChallengeEncryption(t *testing.T) {
	if reverseBits(0x01) != 0x80 || reverseBits(0xF0) != 0x0F || reverseBits(0xAA) != 0x55 {
		t.Fatal("reverseBits is wrong")
	}

	challenge := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F,
	}
	response, err := vncEncryptChallenge("secretpw", challenge)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(response, challenge) {
		t.Fatal("response should differ from challenge")
	}

	key := []byte("secretpw")
	for i := range key {
		key[i] = reverseBits(key[i])
	}
	cipher, err := des.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	decrypted := make([]byte, 16)
	cipher.Decrypt(decrypted[0:8], response[0:8])
	cipher.Decrypt(decrypted[8:16], response[8:16])
	if !bytes.Equal(decrypted, challenge) {
		t.Fatal("round trip failed")
	}
}

// TestModifierKeysym pins the inverted Apple/OSXvnc modifier mapping that is
// required for Cmd/Option shortcuts (Spotlight, the SSH-enable sequence) to
// work: Command must be sent as Alt_L (0xFFE9), Option as Meta_L (0xFFE7).

func TestKeysymForRune(t *testing.T) {
	cases := []struct {
		in        rune
		keysym    uint32
		needShift bool
	}{
		{'a', 'a', false},
		{'N', 'n', true}, // uppercase -> lowercase keysym + shift
		{'Z', 'z', true},
		{'3', '3', false},
		{'-', '-', false},
		{' ', KeysymSpace, false},
		{'_', 0x2d, true}, // shifted symbol -> unshifted key keysym + shift
		{'@', 0x32, true},
	}
	for _, c := range cases {
		keysym, needShift, ok := KeysymForRune(c.in)
		if !ok || keysym != c.keysym || needShift != c.needShift {
			t.Errorf("KeysymForRune(%q) = (%#x, %v, %v), want (%#x, %v, true)",
				c.in, keysym, needShift, ok, c.keysym, c.needShift)
		}
	}
}
