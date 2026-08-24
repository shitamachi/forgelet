package secret

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFileKeyringHex(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	hexStr := hex.EncodeToString(key)
	path := filepath.Join(dir, "key.hex")
	if err := os.WriteFile(path, []byte(hexStr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kr, err := NewFileKeyring(path)
	if err != nil {
		t.Fatalf("NewFileKeyring hex: %v", err)
	}
	k, err := kr.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if k.Version != 1 || hex.EncodeToString(k.Material) != hexStr {
		t.Errorf("key mismatch")
	}
	// Verify cipher works with this ring
	c := NewCipher(kr, nil)
	sealed, err := c.Seal([]byte("hello"), SecretAAD("repository", "TEST"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	plain, err := c.Open(sealed, SecretAAD("repository", "TEST"))
	if err != nil || string(plain) != "hello" {
		t.Fatalf("Open: %v %q", err, string(plain))
	}
}

func TestFileKeyringRaw(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(255 - i)
	}
	path := filepath.Join(dir, "key.raw")
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	kr, err := NewFileKeyring(path)
	if err != nil {
		t.Fatalf("NewFileKeyring raw: %v", err)
	}
	k, _ := kr.Current()
	if string(k.Material) != string(key) {
		t.Error("raw material mismatch")
	}
}

func TestFileKeyringMissing(t *testing.T) {
	_, err := NewFileKeyring("/nonexistent/path")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
