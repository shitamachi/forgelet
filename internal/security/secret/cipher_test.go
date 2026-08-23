package secret

import (
	"bytes"
	"strings"
	"testing"
)

func key(version uint32, b byte) Key {
	material := bytes.Repeat([]byte{b}, 32)
	material[0] = byte(version)
	return Key{Version: version, Material: material}
}

func ring(t *testing.T, current uint32, keys ...Key) *StaticKeyring {
	t.Helper()
	r, err := NewStaticKeyring(current, keys...)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return r
}

func TestKeyringValidation(t *testing.T) {
	if _, err := NewStaticKeyring(1); err == nil {
		t.Error("empty keyring must fail")
	}
	if _, err := NewStaticKeyring(2, key(1, 0x01)); err == nil {
		t.Error("current not among keys must fail")
	}
	if _, err := NewStaticKeyring(1, key(1, 0x01), key(1, 0x02)); err == nil {
		t.Error("duplicate version must fail")
	}
	if _, err := NewStaticKeyring(1, Key{Version: 1, Material: make([]byte, 16)}); err == nil {
		t.Error("short material must fail")
	}
	if _, err := NewStaticKeyring(1, Key{Version: 0, Material: make([]byte, 32)}); err == nil {
		t.Error("version 0 must fail")
	}
}

func TestSealOpenRoundtrip(t *testing.T) {
	c := NewCipher(ring(t, 1, key(1, 0x01)), nil)
	aad := SecretAAD("repository", "REGISTRY_TOKEN")

	sealed, err := c.Seal([]byte("super-secret-value"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("super-secret-value")) ||
		bytes.Contains(sealed.WrappedDEK, []byte("super-secret-value")) {
		t.Fatal("plaintext leaked into sealed form")
	}
	got, err := c.Open(sealed, aad)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(got) != "super-secret-value" {
		t.Fatalf("roundtrip got %q", got)
	}
	if sealed.MasterKeyVersion != 1 {
		t.Errorf("sealed with version %d, want 1", sealed.MasterKeyVersion)
	}
}

func TestOpenDetectsTampering(t *testing.T) {
	c := NewCipher(ring(t, 1, key(1, 0x01)), nil)
	aad := SecretAAD("repository", "TOKEN")
	sealed, err := c.Seal([]byte("value"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	flip := func(b []byte) []byte {
		out := append([]byte(nil), b...)
		out[0] ^= 0xFF
		return out
	}

	tampered := sealed
	tampered.Ciphertext = flip(sealed.Ciphertext)
	if _, err := c.Open(tampered, aad); err == nil {
		t.Error("payload tampering accepted")
	}

	tampered = sealed
	tampered.WrappedDEK = flip(sealed.WrappedDEK)
	if _, err := c.Open(tampered, aad); err == nil {
		t.Error("wrapped DEK tampering accepted")
	}

	tampered = sealed
	tampered.WrappedDEK = tampered.WrappedDEK[:5]
	if _, err := c.Open(tampered, aad); err == nil {
		t.Error("truncated wrapped DEK accepted")
	}

	tampered = sealed
	tampered.MasterKeyVersion = 9
	if _, err := c.Open(tampered, aad); err == nil {
		t.Error("unknown key version accepted")
	} else if !strings.Contains(err.Error(), "version 9") {
		t.Errorf("missing-version error lacks version: %v", err)
	}

	// AAD swap: same name in another scope, and another name.
	if _, err := c.Open(sealed, SecretAAD("environment", "TOKEN")); err == nil {
		t.Error("scope swap accepted")
	}
	if _, err := c.Open(sealed, SecretAAD("repository", "OTHER")); err == nil {
		t.Error("name swap accepted")
	}
	if _, err := c.Open(sealed, nil); err == nil {
		t.Error("missing AAD accepted")
	}
}

func TestSealUsesCurrentVersion(t *testing.T) {
	c := NewCipher(ring(t, 2, key(1, 0x01), key(2, 0x02)), nil)
	aad := SecretAAD("repository", "TOKEN")
	sealed, err := c.Seal([]byte("v"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealed.MasterKeyVersion != 2 {
		t.Fatalf("sealed with version %d, want current 2", sealed.MasterKeyVersion)
	}
}

func TestRewrapAfterRotation(t *testing.T) {
	oldRing := ring(t, 1, key(1, 0x01))
	c1 := NewCipher(oldRing, nil)
	aad := SecretAAD("repository", "TOKEN")
	sealedV1, err := c1.Seal([]byte("rotate-me"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Rotate: version 2 becomes current, version 1 retained.
	newRing := ring(t, 2, key(1, 0x01), key(2, 0x02))
	c2 := NewCipher(newRing, nil)

	// Old ciphertext stays readable.
	got, err := c2.Open(sealedV1, aad)
	if err != nil || string(got) != "rotate-me" {
		t.Fatalf("old ciphertext unreadable after rotation: %v", err)
	}

	// Rewrap moves it to the current version and stays readable.
	sealedV2, err := c2.Rewrap(sealedV1, aad)
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if sealedV2.MasterKeyVersion != 2 {
		t.Fatalf("rewrap kept version %d, want 2", sealedV2.MasterKeyVersion)
	}
	if got, err := c2.Open(sealedV2, aad); err != nil || string(got) != "rotate-me" {
		t.Fatalf("rewrapped ciphertext unreadable: %v", err)
	}

	// Retired version: key 1 dropped from the ring — old data fails until
	// migrated, new data sealed with version 2.
	retiredRing := ring(t, 2, key(2, 0x02))
	c3 := NewCipher(retiredRing, nil)
	if _, err := c3.Open(sealedV1, aad); err == nil {
		t.Error("retired version must not decrypt")
	}
	fresh, err := c3.Seal([]byte("new"), aad)
	if err != nil || fresh.MasterKeyVersion != 2 {
		t.Fatalf("seal after retirement: %v %+v", err, fresh)
	}
}

func TestRewrapRefusesCorruptInput(t *testing.T) {
	c := NewCipher(ring(t, 1, key(1, 0x01)), nil)
	aad := SecretAAD("repository", "TOKEN")
	if _, err := c.Rewrap(Sealed{}, aad); err == nil {
		t.Fatal("rewrap of empty sealed value must fail")
	}
}

func TestSecretAADDistinct(t *testing.T) {
	a := SecretAAD("repository", "T")
	if string(a) == string(SecretAAD("environment", "T")) ||
		string(a) == string(SecretAAD("repository", "U")) {
		t.Fatal("AAD collision across identities")
	}
}
