package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/shitamachi/forgelet/internal/run"
)

var settingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func secretCipher(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets require a 32-byte encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func sealSecret(key []byte, repository, name, value string) ([]byte, error) {
	aead, err := secretCipher(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(value), []byte(repository+"\x00"+name)), nil
}
func openSecret(key []byte, repository, name string, data []byte) (string, error) {
	aead, err := secretCipher(key)
	if err != nil {
		return "", err
	}
	if len(data) < aead.NonceSize() {
		return "", fmt.Errorf("invalid encrypted secret")
	}
	nonce := data[:aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, data[aead.NonceSize():], []byte(repository+"\x00"+name))
	if err != nil {
		return "", fmt.Errorf("cannot decrypt secret with configured key")
	}
	return string(plain), nil
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	repository := chi.URLParam(r, "owner") + "/" + chi.URLParam(r, "repo")
	kind, name := chi.URLParam(r, "kind"), chi.URLParam(r, "name")
	if !s.allowed(repository) || (kind != "vars" && kind != "secrets") {
		http.Error(w, "unknown repository or setting kind", 404)
		return
	}
	if name != "" && (!settingName.MatchString(name) || strings.HasPrefix(name, "GITHUB_") || strings.HasPrefix(name, "FORGELET_")) {
		http.Error(w, "invalid or reserved setting name", 400)
		return
	}
	switch r.Method {
	case http.MethodGet:
		values, err := s.store.Settings(r.Context(), repository, kind)
		if err != nil {
			serverError(w, err)
			return
		}
		if kind == "secrets" {
			names := make([]string, 0, len(values))
			for name := range values {
				names = append(names, name)
			}
			sort.Strings(names)
			writeJSON(w, 200, map[string]any{"names": names})
			return
		}
		plain := map[string]string{}
		for name, value := range values {
			plain[name] = string(value)
		}
		writeJSON(w, 200, plain)
	case http.MethodDelete:
		respond(w, map[string]bool{"deleted": true}, s.store.DeleteSetting(r.Context(), repository, kind, name))
	case http.MethodPut:
		var input struct {
			Value string `json:"value"`
		}
		if err := decode(w, r, &input, 20<<10); err != nil {
			http.Error(w, "invalid setting body", 400)
			return
		}
		if len(input.Value) > 16<<10 || strings.ContainsRune(input.Value, 0) {
			http.Error(w, "setting value exceeds 16 KiB or contains NUL", 400)
			return
		}
		if kind == "secrets" && len(input.Value) > 4096 {
			http.Error(w, "secret exceeds the 4 KiB masking limit", http.StatusBadRequest)
			return
		}
		value := []byte(input.Value)
		if kind == "secrets" {
			if len(s.cfg.SecretKey) != 32 {
				http.Error(w, "secret encryption key not configured", http.StatusServiceUnavailable)
				return
			}
			var err error
			value, err = sealSecret(s.cfg.SecretKey, repository, name, input.Value)
			if err != nil {
				http.Error(w, "cannot encrypt secret", 500)
				return
			}
		}
		respond(w, map[string]bool{"saved": true}, s.store.PutSetting(r.Context(), repository, kind, name, value))
	}
}
func (s *Server) runtimeSecrets(ctx context.Context, plan run.Plan) (map[string]string, error) {
	result := map[string]string{}
	if len(plan.SecretNames) == 0 {
		return result, nil
	}
	values, err := s.store.Settings(ctx, plan.Repository, "secrets")
	if err != nil {
		return nil, err
	}
	for _, name := range plan.SecretNames {
		if data, ok := values[name]; ok {
			plain, err := openSecret(s.cfg.SecretKey, plan.Repository, name, data)
			if err != nil {
				return nil, err
			}
			result[name] = plain
		} else {
			result[name] = ""
		}
	}
	return result, nil
}
