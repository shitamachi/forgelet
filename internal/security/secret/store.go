package secret

import "context"

// Info identifies a stored secret without its value.
type Info struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

// Store is the durable sealed-secret port used by the control plane.
type Store interface {
	GetSecret(ctx context.Context, scope, name string) (string, error)
	PutSecret(ctx context.Context, scope, name, value string) error
	DeleteSecret(ctx context.Context, scope, name string) error
	ListSecrets(ctx context.Context) ([]Info, error)
}
