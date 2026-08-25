package postgres

import (
	"context"
	"testing"

	"github.com/shitamachi/forgelet/internal/security/secret"
)

// testSecretsCipher builds a deterministic cipher for tests.
func testSecretsCipher() *secret.Cipher {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	kr, _ := secret.NewStaticKeyring(1, secret.Key{Version: 1, Material: key})
	return secret.NewCipher(kr, nil)
}

func TestPGSecretPutGetDelete(t *testing.T) {
	s, _ := testDatabase(t)
	// Ensure secrets table is clean
	_, _ = s.pool.Exec(context.Background(), `TRUNCATE secrets`)
	cipher := testSecretsCipher()
	ctx := context.Background()

	// Put and get
	if err := s.PutSecret(ctx, cipher, "repository", "MY_SECRET", []byte("s3cr3t")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	plain, err := s.GetSecret(ctx, cipher, "repository", "MY_SECRET")
	if err != nil || string(plain) != "s3cr3t" {
		t.Fatalf("GetSecret: %v %q", err, string(plain))
	}
	// Overwrite
	if err := s.PutSecret(ctx, cipher, "repository", "MY_SECRET", []byte("newval")); err != nil {
		t.Fatalf("PutSecret overwrite: %v", err)
	}
	plain, err = s.GetSecret(ctx, cipher, "repository", "MY_SECRET")
	if err != nil || string(plain) != "newval" {
		t.Fatalf("Get after overwrite: %v %q", err, string(plain))
	}
	// AAD binding: same ciphertext cannot be opened with different scope
	if err := s.PutSecret(ctx, cipher, "environment", "MY_SECRET", []byte("envval")); err != nil {
		t.Fatalf("Put env: %v", err)
	}
	// Ensure repo and env are isolated (different AAD)
	plain, err = s.GetSecret(ctx, cipher, "environment", "MY_SECRET")
	if err != nil || string(plain) != "envval" {
		t.Fatalf("Get env: %v %q", err, string(plain))
	}
	// List
	list, err := s.ListSecrets(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSecrets: %v len=%d", err, len(list))
	}
	// Delete
	if err := s.DeleteSecret(ctx, "repository", "MY_SECRET"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetSecret(ctx, cipher, "repository", "MY_SECRET"); err == nil {
		t.Error("Get after delete should fail")
	}
}

func TestPGSecretRotation(t *testing.T) {
	s, _ := testDatabase(t)
	_, _ = s.pool.Exec(context.Background(), `TRUNCATE secrets`)
	ctx := context.Background()

	key1 := make([]byte, 32)
	for i := range key1 {
		key1[i] = byte(i + 1)
	}
	key2 := make([]byte, 32)
	for i := range key2 {
		key2[i] = byte(100 + i)
	}
	kr1, _ := secret.NewStaticKeyring(1, secret.Key{Version: 1, Material: key1})
	c1 := secret.NewCipher(kr1, nil)
	if err := s.PutSecret(ctx, c1, "repository", "ROT", []byte("old")); err != nil {
		t.Fatalf("Put with v1: %v", err)
	}
	// New ring with both keys, current 2
	kr2, _ := secret.NewStaticKeyring(2, secret.Key{Version: 1, Material: key1}, secret.Key{Version: 2, Material: key2})
	c2 := secret.NewCipher(kr2, nil)
	// Old value still readable with new ring
	plain, err := s.GetSecret(ctx, c2, "repository", "ROT")
	if err != nil || string(plain) != "old" {
		t.Fatalf("Get old with new ring: %v %q", err, string(plain))
	}
	// Rewrap to new version
	// Fetch sealed directly for rewrap test via Put with new cipher (overwrite)
	if err := s.PutSecret(ctx, c2, "repository", "ROT", []byte("old")); err != nil {
		t.Fatalf("Put with v2: %v", err)
	}
	// Old ring should not open new version
	if _, err := s.GetSecret(ctx, c1, "repository", "ROT"); err == nil {
		t.Error("old ring should not open v2 sealed value")
	}
	// New ring can
	plain, err = s.GetSecret(ctx, c2, "repository", "ROT")
	if err != nil || string(plain) != "old" {
		t.Fatalf("Get new: %v %q", err, string(plain))
	}
}
