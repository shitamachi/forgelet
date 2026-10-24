package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

const baseConfig = `internal_url: https://forgelet.example.test
profiles:
  small:
    image: forgelet-executor:test
`

func configFile(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forgelet.yaml")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaultsAndParallelism(t *testing.T) {
	cfg, err := loadConfig(configFile(t, baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxParallel != 4 || cfg.Listen != ":8080" || cfg.DataDir != "./data" || cfg.Namespace != "forgelet" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"1", true}, {"2", true}, {"1024", true},
		{"0", false}, {"-1", false}, {"1025", false}, {"four", false}, {"1.5", false}, {"null", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			_, err := loadConfig(configFile(t, baseConfig+"max_parallel: "+tc.value+"\n"))
			if (err == nil) != tc.valid {
				t.Fatalf("max_parallel=%s error=%v; valid=%v", tc.value, err, tc.valid)
			}
		})
	}
}

func TestLoadConfigStrictDocuments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
	}{
		{"unknown", baseConfig + "unrecognized_option: 2\n"},
		{"nested unknown", strings.Replace(baseConfig, "    image:", "    misspelled: true\n    image:", 1)},
		{"two docs", baseConfig + "---\nmax_parallel: 1\n"},
		{"empty second doc", baseConfig + "---\n"},
		{"duplicate key", baseConfig + "max_parallel: 1\nmax_parallel: 2\n"},
		{"empty", ""},
		{"null", "null\n"},
		{"invalid yaml", "["},
		{"too large", baseConfig + "#" + strings.Repeat("x", 1<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadConfig(configFile(t, tc.source)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestLoadConfigRuntimeTLSAndProfileValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		valid  bool
	}{
		{"HTTPS", baseConfig, true},
		{"open management", baseConfig + "admin_access: open\n", true},
		{"token management", baseConfig + "admin_access: token\n", true},
		{"invalid management", baseConfig + "admin_access: disabled\n", false},
		{"HTTP denied", strings.Replace(baseConfig, "https://", "http://", 1), false},
		{"HTTP explicit", strings.Replace(baseConfig, "https://", "http://", 1) + "allow_insecure_runtime: true\n", true},
		{"HTTP false", strings.Replace(baseConfig, "https://", "http://", 1) + "allow_insecure_runtime: false\n", false},
		{"unsupported scheme", strings.Replace(baseConfig, "https://", "ftp://", 1) + "allow_insecure_runtime: true\n", false},
		{"relative URL", strings.Replace(baseConfig, "https://forgelet.example.test", "/relative", 1), false},
		{"URL credentials", strings.Replace(baseConfig, "https://", "https://user:password@", 1), false},
		{"URL fragment", strings.Replace(baseConfig, "https://forgelet.example.test", "https://forgelet.example.test/#fragment", 1), false},
		{"missing URL", strings.Replace(baseConfig, "internal_url: https://forgelet.example.test\n", "", 1), false},
		{"certificate only", baseConfig + "tls_cert_file: cert.pem\n", false},
		{"key only", baseConfig + "tls_key_file: key.pem\n", false},
		{"pair", baseConfig + "tls_cert_file: cert.pem\ntls_key_file: key.pem\n", true},
		{"no profile", "internal_url: https://forgelet.example.test\n", false},
		{"empty image", strings.Replace(baseConfig, "forgelet-executor:test", "''", 1), false},
		{"zero resources", strings.Replace(baseConfig, "    image:", "    memory_limit: 0\n    image:", 1), false},
		{"invalid listen", baseConfig + "listen: bad-listen\n", false},
		{"empty data", baseConfig + "data_dir: ''\n", false},
		{"invalid public URL", baseConfig + "public_url: /relative\n", false},
		{"App ID without key", baseConfig + "github_app_id: '123'\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfig(configFile(t, tc.source))
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v; valid=%v", err, tc.valid)
			}
		})
	}
}

func TestServeRejectsInvalidConfigurationBeforeExternalSetup(t *testing.T) {
	t.Setenv("FORGELET_ADMIN_TOKEN", "long-enough-administrator-token")
	for _, tc := range []struct {
		name  string
		extra string
		token string
		want  string
	}{
		{"concurrency", "max_parallel: 0\n", "long-enough-administrator-token", "max_parallel"},
		{"TLS pair", "tls_cert_file: missing.pem\n", "long-enough-administrator-token", "configured together"},
		{"missing TLS files", "tls_cert_file: missing.pem\ntls_key_file: missing-key.pem\n", "long-enough-administrator-token", "load TLS"},
		{"admin token", "", "short", "FORGELET_ADMIN_TOKEN"},
		{"GitHub key", "github_app_id: '123'\ngithub_app_key_file: missing.pem\n", "long-enough-administrator-token", "GitHub App key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FORGELET_ADMIN_TOKEN", tc.token)
			dir := filepath.Join(t.TempDir(), "must-not-be-created")
			source := baseConfig + fmt.Sprintf("data_dir: %q\nkubeconfig: %q\n", dir, filepath.Join(t.TempDir(), "never-read-kubeconfig")) + tc.extra
			err := serve(t.Context(), configFile(t, source))
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "kubernetes configuration") {
				t.Fatalf("wrong initialization order: %v; want %q", err, tc.want)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid config touched database directory: %v", err)
			}
		})
	}
}

func TestPrepareRestoreCommandDoesNotStartServer(t *testing.T) {
	t.Setenv("FORGELET_ADMIN_TOKEN", "")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "must-not-be-opened"))
	live, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()
	plan := run.Plan{JobID: "job", RunID: "run"}
	_, err = live.CreateRun(t.Context(), run.Run{ID: "run", DeliveryID: "event", WorkflowPath: "ci.yml"}, []run.Plan{plan})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "backup.db")
	if err := live.Backup(t.Context(), destination); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(t.Context(), []string{"--prepare-restore", destination}); err != nil {
		t.Fatalf("restore-only command: %v", err)
	}
	restored, err := storage.Open(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	job, err := restored.GetJob(t.Context(), "job")
	if err != nil || job.State != run.Interrupted {
		t.Fatalf("restored job=%+v,%v", job, err)
	}
	jobs, err := restored.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("old work resumed=%+v,%v", jobs, err)
	}
}

func TestCommandRejectsAmbiguousOrInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--prepare-restore", ""},
		{"--prepare-restore", "snapshot.db", "--config", "server.yaml"},
		{"unexpected"},
		{"--unknown"},
		{"--prepare-restore", filepath.Join(t.TempDir(), "missing.db")},
	} {
		if err := runCommand(t.Context(), args); err == nil {
			t.Errorf("invalid arguments accepted: %v", args)
		}
	}
	if err := runCommand(t.Context(), []string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help did not exit before server setup: %v", err)
	}
}
