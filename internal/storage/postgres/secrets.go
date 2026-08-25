package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/security/secret"
)

// PutSecret seals plaintext and stores it under scope/name. It overwrites
// any existing entry.
func (s *Store) PutSecret(ctx context.Context, cipher *secret.Cipher, scope, name string, plaintext []byte) error {
	if scope == "" || name == "" {
		return fmt.Errorf("postgres: PutSecret scope and name are required")
	}
	aad := secret.SecretAAD(scope, name)
	sealed, err := cipher.Seal(plaintext, aad)
	if err != nil {
		return fmt.Errorf("postgres: PutSecret seal: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO secrets (scope, name, nonce, ciphertext, wrapped_dek, master_key_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
		ON CONFLICT (scope, name) DO UPDATE SET
			nonce = EXCLUDED.nonce,
			ciphertext = EXCLUDED.ciphertext,
			wrapped_dek = EXCLUDED.wrapped_dek,
			master_key_version = EXCLUDED.master_key_version,
			updated_at = EXCLUDED.updated_at
	`, scope, name, sealed.Nonce, sealed.Ciphertext, sealed.WrappedDEK, sealed.MasterKeyVersion, now)
	if err != nil {
		return fmt.Errorf("postgres: PutSecret: %w", err)
	}
	return nil
}

// GetSecret retrieves and opens the secret at scope/name.
func (s *Store) GetSecret(ctx context.Context, cipher *secret.Cipher, scope, name string) ([]byte, error) {
	var sealed secret.Sealed
	var version uint32
	// Need to scan into int for version then convert
	var ver int
	err := s.pool.QueryRow(ctx, `SELECT nonce, ciphertext, wrapped_dek, master_key_version FROM secrets WHERE scope=$1 AND name=$2`, scope, name).Scan(
		&sealed.Nonce, &sealed.Ciphertext, &sealed.WrappedDEK, &ver)
	if err != nil {
		return nil, fmt.Errorf("postgres: GetSecret %s/%s: %w", scope, name, err)
	}
	sealed.MasterKeyVersion = uint32(ver)
	aad := secret.SecretAAD(scope, name)
	plaintext, err := cipher.Open(sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("postgres: GetSecret open: %w", err)
	}
	version = sealed.MasterKeyVersion
	_ = version
	return plaintext, nil
}

// DeleteSecret removes the secret at scope/name.
func (s *Store) DeleteSecret(ctx context.Context, scope, name string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM secrets WHERE scope=$1 AND name=$2`, scope, name)
	if err != nil {
		return fmt.Errorf("postgres: DeleteSecret: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("postgres: DeleteSecret %s/%s not found", scope, name)
	}
	return nil
}

// ListSecrets returns all secrets' scope/name pairs without opening them.
func (s *Store) ListSecrets(ctx context.Context) ([]struct{ Scope, Name string }, error) {
	rows, err := s.pool.Query(ctx, `SELECT scope, name FROM secrets ORDER BY scope, name`)
	if err != nil {
		return nil, fmt.Errorf("postgres: ListSecrets: %w", err)
	}
	defer rows.Close()
	var out []struct{ Scope, Name string }
	for rows.Next() {
		var sc, nm string
		if err := rows.Scan(&sc, &nm); err != nil {
			return nil, err
		}
		out = append(out, struct{ Scope, Name string }{sc, nm})
	}
	return out, rows.Err()
}

// SecretStore is a server.SecretStore adapter over a Store and Cipher.
type SecretStore struct {
	store  *Store
	cipher *secret.Cipher
}

// NewSecretStore wires a SecretStore.
func NewSecretStore(store *Store, cipher *secret.Cipher) *SecretStore {
	return &SecretStore{store: store, cipher: cipher}
}

// GetSecret implements secret.Store.
func (s *SecretStore) GetSecret(ctx context.Context, scope, name string) (string, error) {
	b, err := s.store.GetSecret(ctx, s.cipher, scope, name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// PutSecret implements secret.Store.
func (s *SecretStore) PutSecret(ctx context.Context, scope, name, value string) error {
	return s.store.PutSecret(ctx, s.cipher, scope, name, []byte(value))
}

// DeleteSecret implements secret.Store.
func (s *SecretStore) DeleteSecret(ctx context.Context, scope, name string) error {
	return s.store.DeleteSecret(ctx, scope, name)
}

// ListSecrets implements secret.Store.
func (s *SecretStore) ListSecrets(ctx context.Context) ([]secret.Info, error) {
	raw, err := s.store.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]secret.Info, 0, len(raw))
	for _, r := range raw {
		out = append(out, secret.Info{Scope: r.Scope, Name: r.Name})
	}
	return out, nil
}
