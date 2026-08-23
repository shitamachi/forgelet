//go:build tools

// Package tools pins code-generator dependencies in go.mod for `go generate`
// and `make generate` (see api/v1alpha1/groupversion_info.go).
package tools

import (
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
