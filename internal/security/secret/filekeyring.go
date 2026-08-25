package secret

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// FileKeyring reads a 32-byte master key from a file. The file may contain
// either raw 32 bytes or a hex-encoded 64-character string (with optional
// newline), as produced by `openssl rand -hex 32` or a K8s Secret mounted
// as a file. The version is fixed to 1 for V1; rotation via multiple
// versioned files is a future extension.
type FileKeyring struct {
	path string
	kr   *StaticKeyring
}

// NewFileKeyring loads the key at path and builds a single-version ring.
func NewFileKeyring(path string) (*FileKeyring, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secret: read key file %q: %w", path, err)
	}
	s := strings.TrimSpace(string(data))
	var material []byte
	if len(s) == 64 {
		// Try hex
		if decoded, err := hex.DecodeString(s); err == nil && len(decoded) == 32 {
			material = decoded
		}
	}
	if material == nil {
		// Fallback: raw bytes (allow 32 raw bytes, possibly with newline)
		// If the file was read as raw 32 bytes, s may have been truncated by TrimSpace
		// but raw bytes may contain non-printable, so check original data length
		if len(data) == 32 {
			material = make([]byte, 32)
			copy(material, data)
		} else if len(data) == 33 && data[32] == '\n' {
			material = make([]byte, 32)
			copy(material, data[:32])
		} else {
			// Try hex decode of trimmed string if not 64? Maybe file contains hex without newline but with 64 chars
			if decoded, err := hex.DecodeString(s); err == nil && len(decoded) == 32 {
				material = decoded
			} else {
				return nil, fmt.Errorf("secret: key file %q must be 32 raw bytes or 64 hex chars, got %d bytes", path, len(data))
			}
		}
	}
	kr, err := NewStaticKeyring(1, Key{Version: 1, Material: material})
	if err != nil {
		return nil, err
	}
	return &FileKeyring{path: path, kr: kr}, nil
}

// Current implements Keyring.
func (f *FileKeyring) Current() (Key, error) { return f.kr.Current() }

// Key implements Keyring.
func (f *FileKeyring) Key(version uint32) (Key, bool) { return f.kr.Key(version) }
