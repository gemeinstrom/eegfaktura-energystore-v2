package decode

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"strings"
	"testing"
)

// helperEncryptForTest mirrors the prod-stack pipeline so tests can use
// realistic vectors WITHOUT depending on real production data: gzip →
// PKCS#7 pad → AES-CBC encrypt → base64. Uses a synthetic test key/iv,
// NOT the real prod-stack key.
func helperEncryptForTest(t *testing.T, plaintext []byte, key, iv []byte, useGzip bool) []byte {
	t.Helper()

	body := plaintext
	if useGzip {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(plaintext); err != nil {
			t.Fatalf("test gzip write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("test gzip close: %v", err)
		}
		body = buf.Bytes()
	}

	pad := aes.BlockSize - len(body)%aes.BlockSize
	padded := append(body, bytes.Repeat([]byte{byte(pad)}, pad)...)

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("test aes new: %v", err)
	}
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	return []byte(base64.StdEncoding.EncodeToString(ct))
}

// Synthetic key/iv for testing — NOT the production key. Documentation
// and security analysis for the real prod-stack key sit in
// gemeinstrom/eegfaktura-platform#170.
var (
	testKey = bytes.Repeat([]byte{0xA5}, 32)
	testIV  = bytes.Repeat([]byte{0x5A}, 16)
)

func TestDecrypt_Disabled_Passthrough(t *testing.T) {
	c := DecryptConfig{}
	if c.Enabled() {
		t.Fatal("empty config should be disabled")
	}
	in := []byte(`{"hello":"world"}`)
	out, err := c.Decrypt(in)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("expected passthrough, got %q", out)
	}
}

func TestDecrypt_WithGzip_RoundTrip(t *testing.T) {
	plain := []byte(`{"messageId":"TEST","energy":{"data":[{"meterCode":"1-1:1.9.0 G.01","value":[]}]}}`)
	enc := helperEncryptForTest(t, plain, testKey, testIV, true)

	c := DecryptConfig{Key: testKey, IV: testIV, Gzip: true}
	got, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(plain, got) {
		t.Fatalf("plaintext mismatch:\n want %s\n got  %s", plain, got)
	}
}

func TestDecrypt_NoGzip_RoundTrip(t *testing.T) {
	plain := []byte(`{"x":"y"}`)
	enc := helperEncryptForTest(t, plain, testKey, testIV, false)

	c := DecryptConfig{Key: testKey, IV: testIV, Gzip: false}
	got, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(plain, got) {
		t.Fatalf("plaintext mismatch:\n want %s\n got  %s", plain, got)
	}
}

func TestDecrypt_WhitespaceTolerant(t *testing.T) {
	plain := []byte(`{"a":1}`)
	enc := helperEncryptForTest(t, plain, testKey, testIV, true)
	// MQTT clients sometimes append newlines.
	enc = []byte("  " + string(enc) + "\n")

	c := DecryptConfig{Key: testKey, IV: testIV, Gzip: true}
	got, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(plain, got) {
		t.Fatalf("plaintext mismatch")
	}
}

func TestDecrypt_RejectsBadBase64(t *testing.T) {
	c := DecryptConfig{Key: testKey, IV: testIV, Gzip: true}
	_, err := c.Decrypt([]byte("not !! base64 ###"))
	if err == nil {
		t.Fatal("expected base64 error")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Fatalf("expected base64 in error, got %v", err)
	}
}

func TestDecrypt_RejectsNonBlockSizeCiphertext(t *testing.T) {
	// "AAAA" base64-decodes to 3 bytes — not block-aligned.
	c := DecryptConfig{Key: testKey, IV: testIV, Gzip: false}
	_, err := c.Decrypt([]byte("AAAA"))
	if err == nil {
		t.Fatal("expected block-size error")
	}
	if !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("expected multiple-of-block-size error, got %v", err)
	}
}

func TestDecrypt_RejectsWrongKey(t *testing.T) {
	plain := []byte(`{"hi":"there"}`)
	enc := helperEncryptForTest(t, plain, testKey, testIV, false)

	wrongKey := bytes.Repeat([]byte{0xFF}, 32)
	c := DecryptConfig{Key: wrongKey, IV: testIV, Gzip: false}
	// CBC with wrong key produces garbage; PKCS#7 unpad will almost
	// certainly reject it. (Tiny chance of accidental valid padding —
	// not relevant for unit-test scope.)
	_, err := c.Decrypt(enc)
	if err == nil {
		t.Fatal("expected padding/decrypt error with wrong key")
	}
}

func TestDecrypt_RejectsShortKey(t *testing.T) {
	// 32 bytes = 1 AES block, base64 = 44 chars + padding "="
	blockAlignedB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 32))
	c := DecryptConfig{Key: []byte{1, 2, 3}, IV: testIV, Gzip: false}
	_, err := c.Decrypt([]byte(blockAlignedB64))
	if err == nil || !strings.Contains(err.Error(), "aes key") {
		t.Fatalf("expected aes key error, got %v", err)
	}
}

func TestDecrypt_RejectsWrongIVLength(t *testing.T) {
	blockAlignedB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 32))
	c := DecryptConfig{Key: testKey, IV: []byte{1, 2, 3}, Gzip: false}
	_, err := c.Decrypt([]byte(blockAlignedB64))
	if err == nil || !strings.Contains(err.Error(), "iv length") {
		t.Fatalf("expected iv length error, got %v", err)
	}
}

func TestParseHexKey(t *testing.T) {
	cases := map[string]struct {
		want    []byte
		wantErr bool
	}{
		"":           {nil, false},
		"  ":         {nil, false},
		"deadBEEF":   {[]byte{0xDE, 0xAD, 0xBE, 0xEF}, false},
		"0xdeadbeef": {[]byte{0xDE, 0xAD, 0xBE, 0xEF}, false},
		" 0xDE AD ":  {nil, true}, // internal whitespace = invalid
		"zz":         {nil, true},
	}
	for in, want := range cases {
		got, err := ParseHexKey(in)
		if (err != nil) != want.wantErr {
			t.Errorf("ParseHexKey(%q): err=%v, wantErr=%v", in, err, want.wantErr)
			continue
		}
		if !want.wantErr && !bytes.Equal(got, want.want) {
			t.Errorf("ParseHexKey(%q): got %x, want %x", in, got, want.want)
		}
	}
}
