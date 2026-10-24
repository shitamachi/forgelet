package executor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func maskRegistry(directory string, mask *masker) error {
	if directory == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(directory, "config.json"))
	if err != nil {
		return fmt.Errorf("registry credentials unavailable")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return fmt.Errorf("invalid registry credentials")
	}
	var configuration struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
			RegistryToken string `json:"registrytoken"`
			Password      string `json:"password"`
		} `json:"auths"`
	}
	if json.Unmarshal(data, &configuration) != nil {
		return fmt.Errorf("invalid registry credentials")
	}
	for _, auth := range configuration.Auths {
		values := []string{auth.Auth, auth.IdentityToken, auth.RegistryToken, auth.Password}
		if raw, err := base64.StdEncoding.DecodeString(auth.Auth); err == nil {
			if _, password, ok := strings.Cut(string(raw), ":"); ok {
				values = append(values, password)
			}
		}
		for _, value := range values {
			if err := mask.add(value); err != nil {
				return fmt.Errorf("register registry credential mask: %w", err)
			}
		}
	}
	return nil
}
