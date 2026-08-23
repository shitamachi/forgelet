// Package secret implements envelope encryption for secrets at rest: each
// secret is encrypted with a random data key via AES-256-GCM, and the data
// key is wrapped by a versioned master key from a Keyring. The GCM AAD binds
// ciphertext to the secret's identity so sealed blobs cannot be swapped.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Key is a versioned 32-byte master key (AES-256).
type Key struct {
	Version  uint32
	Material []byte
}

// Keyring supplies versioned master keys. Current is used to seal; any
// retained version is used to open.
type Keyring interface {
	Current() (Key, error)
	Key(version uint32) (Key, bool)
}

// StaticKeyring is a fixed in-process keyring built from configuration.
type StaticKeyring struct {
	keys       map[uint32]Key
	currentVer uint32
}

// NewStaticKeyring builds a keyring. Every key must have distinct, non-zero
// versions and 32-byte material; current must be one of the keys.
func NewStaticKeyring(current uint32, keys ...Key) (*StaticKeyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("secret: keyring needs at least one key")
	}
	kr := &StaticKeyring{keys: map[uint32]Key{}, currentVer: current}
	for _, k := range keys {
		if k.Version == 0 {
			return nil, errors.New("secret: key version 0 is reserved")
		}
		if len(k.Material) != 32 {
			return nil, fmt.Errorf("secret: key %d: material must be 32 bytes, got %d", k.Version, len(k.Material))
		}
		if _, dup := kr.keys[k.Version]; dup {
			return nil, fmt.Errorf("secret: duplicate key version %d", k.Version)
		}
		kr.keys[k.Version] = Key{Version: k.Version, Material: append([]byte(nil), k.Material...)}
	}
	if _, ok := kr.keys[current]; !ok {
		return nil, fmt.Errorf("secret: current version %d not among keys", current)
	}
	return kr, nil
}

// Current implements Keyring.
func (r *StaticKeyring) Current() (Key, error) { return r.keys[r.currentVer], nil }

// Key implements Keyring.
func (r *StaticKeyring) Key(version uint32) (Key, bool) {
	k, ok := r.keys[version]
	return k, ok
}

// Sealed is the persisted form of a secret. It never contains plaintext or
// an unwrapped data key.
type Sealed struct {
	// Nonce is the AES-GCM nonce of the payload.
	Nonce []byte
	// Ciphertext is the GCM-sealed payload.
	Ciphertext []byte
	// WrappedDEK is the data key sealed by the master key; its layout is
	// wrap-nonce (12 bytes) || wrapped key material.
	WrappedDEK []byte
	// MasterKeyVersion identifies which keyring version wraps the DEK.
	MasterKeyVersion uint32
}

// Cipher seals and opens secrets with envelope encryption.
type Cipher struct {
	ring Keyring
	rng  io.Reader
}

// NewCipher wires a Cipher over a keyring. A nil rng uses crypto/rand.
func NewCipher(ring Keyring, rng io.Reader) *Cipher {
	if rng == nil {
		rng = rand.Reader
	}
	return &Cipher{ring: ring, rng: rng}
}

// Seal encrypts plaintext under a fresh data key wrapped by the current
// master key. aad binds the ciphertext to its identity.
func (c *Cipher) Seal(plaintext, aad []byte) (Sealed, error) {
	mk, err := c.ring.Current()
	if err != nil {
		return Sealed{}, fmt.Errorf("secret: current master key: %w", err)
	}
	dek := make([]byte, 32)
	if _, err := io.ReadFull(c.rng, dek); err != nil {
		return Sealed{}, fmt.Errorf("secret: data key: %w", err)
	}
	payloadGCM, err := newGCM(dek)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, payloadGCM.NonceSize())
	if _, err := io.ReadFull(c.rng, nonce); err != nil {
		return Sealed{}, fmt.Errorf("secret: payload nonce: %w", err)
	}
	ciphertext := payloadGCM.Seal(nil, nonce, plaintext, aad)

	wrapGCM, err := newGCM(mk.Material)
	if err != nil {
		return Sealed{}, err
	}
	wrapNonce := make([]byte, wrapGCM.NonceSize())
	if _, err := io.ReadFull(c.rng, wrapNonce); err != nil {
		return Sealed{}, fmt.Errorf("secret: wrap nonce: %w", err)
	}
	wrapped := wrapGCM.Seal(nil, wrapNonce, dek, aad)

	return Sealed{
		Nonce:            nonce,
		Ciphertext:       ciphertext,
		WrappedDEK:       append(append([]byte(nil), wrapNonce...), wrapped...),
		MasterKeyVersion: mk.Version,
	}, nil
}

// Open decrypts a Sealed value. It fails on tampering, missing key versions
// and AAD mismatches.
func (c *Cipher) Open(s Sealed, aad []byte) ([]byte, error) {
	mk, ok := c.ring.Key(s.MasterKeyVersion)
	if !ok {
		return nil, fmt.Errorf("secret: master key version %d not available", s.MasterKeyVersion)
	}
	wrapGCM, err := newGCM(mk.Material)
	if err != nil {
		return nil, err
	}
	ns := wrapGCM.NonceSize()
	if len(s.WrappedDEK) < ns+1 {
		return nil, errors.New("secret: wrapped data key too short")
	}
	dek, err := wrapGCM.Open(nil, s.WrappedDEK[:ns], s.WrappedDEK[ns:], aad)
	if err != nil {
		return nil, fmt.Errorf("secret: unwrap data key: %w", err)
	}
	payloadGCM, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	if len(s.Nonce) != payloadGCM.NonceSize() {
		return nil, errors.New("secret: payload nonce size mismatch")
	}
	plaintext, err := payloadGCM.Open(nil, s.Nonce, s.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("secret: open payload: %w", err)
	}
	return plaintext, nil
}

// Rewrap decrypts s and seals it again under the current master key (fresh
// data key and nonces). Use after rotating the keyring's current version.
func (c *Cipher) Rewrap(s Sealed, aad []byte) (Sealed, error) {
	plaintext, err := c.Open(s, aad)
	if err != nil {
		return Sealed{}, fmt.Errorf("secret: rewrap: %w", err)
	}
	return c.Seal(plaintext, aad)
}

// SecretAAD binds a sealed secret to its identity: same name in a different
// scope (or a different name) must fail to open.
func SecretAAD(scope, name string) []byte {
	return []byte("forgelet-secret\x00" + scope + "\x00" + name)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: init gcm: %w", err)
	}
	return gcm, nil
}
