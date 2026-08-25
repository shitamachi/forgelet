package server_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/shitamachi/forgelet/internal/security/secret"
)

// memStore is an in-memory secret.Store for testing.
type memStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemStore() *memStore { return &memStore{data: map[string]string{}} }

func (m *memStore) GetSecret(_ context.Context, scope, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[scope+"/"+name]
	if !ok {
		return "", ioError("not found")
	}
	return v, nil
}
func (m *memStore) PutSecret(_ context.Context, scope, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[scope+"/"+name] = value
	return nil
}
func (m *memStore) DeleteSecret(_ context.Context, scope, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, scope+"/"+name)
	return nil
}
func (m *memStore) ListSecrets(_ context.Context) ([]secret.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []secret.Info
	for k := range m.data {
		parts := strings.SplitN(k, "/", 2)
		if len(parts) != 2 {
			continue
		}
		out = append(out, secret.Info{Scope: parts[0], Name: parts[1]})
	}
	return out, nil
}

type ioError string

func (e ioError) Error() string { return string(e) }

// TestSecretsAPI exercises the management endpoints without PG.
func TestSecretsAPI(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()
	if err := store.PutSecret(ctx, "repository", "MY_SECRET", "value1"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	v, err := store.GetSecret(ctx, "repository", "MY_SECRET")
	if err != nil || v != "value1" {
		t.Fatalf("Get: %v %q", err, v)
	}
	list, err := store.ListSecrets(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v %v", err, list)
	}
	if err := store.DeleteSecret(ctx, "repository", "MY_SECRET"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.GetSecret(ctx, "repository", "MY_SECRET"); err == nil {
		t.Error("Get after delete should fail")
	}
	// Verify that the same store can be used for executor secrets
	_ = store.PutSecret(ctx, "repository", "TOKEN", "secret123")
	if v, _ := store.GetSecret(ctx, "repository", "TOKEN"); v != "secret123" {
		t.Errorf("Get after Put: %q", v)
	}
}
