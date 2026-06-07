package decode

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// DecryptConfig configures an optional pre-decode step for MQTT payloads.
// The prod-eegfaktura-v1 stack wraps CR_MSG payloads in AES-256-CBC +
// gzip + base64 (RE'd 2026-06-07; see gemeinstrom/eegfaktura-platform#169
// for the full analysis). v2 must replicate this so it can drop into
// the production cluster as a v1-replacement.
//
// In the pilot and any non-prod-cluster setup, DecryptConfig is empty
// (Enabled()==false) and Decrypt is a no-op pass-through. The prod
// cluster sets the key + IV via env vars (see config.go), the optional
// gzip flag defaults to true for prod-compat.
type DecryptConfig struct {
	Key  []byte
	IV   []byte
	Gzip bool
}

// Enabled reports whether the decryption layer is active.
// Empty key means: pass through plain JSON payloads (pilot default).
func (c DecryptConfig) Enabled() bool {
	return len(c.Key) > 0
}

// Decrypt unwraps a base64+AES-256-CBC+optionally-gzip payload back to
// the underlying JSON bytes. When Enabled() is false, the input is
// returned unchanged.
//
// Pipeline (matches v1 prod-stack pipeline reverse-engineered from
// xp-adapter Scala / energystore Go binaries):
//
//	input bytes (base64-text)
//	  → base64 decode
//	  → AES-256-CBC decrypt with (Key, IV)
//	  → PKCS#7 unpad
//	  → (optional) gunzip
//	  → output bytes (JSON)
func (c DecryptConfig) Decrypt(payload []byte) ([]byte, error) {
	if !c.Enabled() {
		return payload, nil
	}

	// 1. Base64 decode. Strip whitespace/newlines that MQTT clients
	// sometimes inject around the payload.
	trimmed := bytes.TrimSpace(payload)
	ct, err := base64.StdEncoding.DecodeString(string(trimmed))
	if err != nil {
		// Fall back to RawStd in case the producer omits padding.
		ct, err = base64.RawStdEncoding.DecodeString(string(trimmed))
		if err != nil {
			return nil, fmt.Errorf("decode/decrypt: base64: %w", err)
		}
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("decode/decrypt: ciphertext length %d not a multiple of %d", len(ct), aes.BlockSize)
	}

	// 2. AES-CBC decrypt. Note: this implementation uses the configured
	// static IV — this matches the prod-stack behaviour exactly but is
	// cryptographically weak; see security issue platform#170 for the
	// modernisation roadmap (per-message random IV, AES-GCM).
	block, err := aes.NewCipher(c.Key)
	if err != nil {
		return nil, fmt.Errorf("decode/decrypt: aes key (got %d bytes): %w", len(c.Key), err)
	}
	if len(c.IV) != aes.BlockSize {
		return nil, fmt.Errorf("decode/decrypt: iv length %d, want %d", len(c.IV), aes.BlockSize)
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, c.IV).CryptBlocks(pt, ct)

	// 3. PKCS#7 unpad. Java's "PKCS5Padding" is identical to PKCS#7
	// at 16-byte block size — the JCE name is historical.
	if len(pt) == 0 {
		return nil, fmt.Errorf("decode/decrypt: empty plaintext")
	}
	padLen := int(pt[len(pt)-1])
	if padLen <= 0 || padLen > aes.BlockSize || padLen > len(pt) {
		return nil, fmt.Errorf("decode/decrypt: invalid PKCS#7 padding length %d", padLen)
	}
	// Constant-time-ish verification of the padding bytes.
	for i := len(pt) - padLen; i < len(pt); i++ {
		if pt[i] != byte(padLen) {
			return nil, fmt.Errorf("decode/decrypt: corrupt PKCS#7 padding at byte %d", i)
		}
	}
	pt = pt[:len(pt)-padLen]

	// 4. Optional gunzip.
	if !c.Gzip {
		return pt, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(pt))
	if err != nil {
		return nil, fmt.Errorf("decode/decrypt: gzip reader: %w", err)
	}
	defer r.Close()
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decode/decrypt: gunzip: %w", err)
	}
	return plain, nil
}

// ParseHexKey decodes a hex-encoded byte string, tolerating surrounding
// whitespace and optional 0x prefix. Used to read ESV2_MQTT_DECRYPT_KEY_HEX
// and ESV2_MQTT_DECRYPT_IV_HEX from environment / config.
func ParseHexKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return nil, nil
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("parse hex key: %w", err)
	}
	return out, nil
}
