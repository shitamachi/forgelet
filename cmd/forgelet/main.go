// Command forgelet runs the single-process control plane.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/kube"
	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage"
)

type authConfig struct {
	BootstrapPasswordFile  string `yaml:"bootstrap_password_file"`
	GitHubClientID         string `yaml:"github_client_id"`
	GitHubClientSecretFile string `yaml:"github_client_secret_file"`
	GitHubWebURL           string `yaml:"github_web_url"`
}
type config struct {
	ActionAPIURL         string                     `yaml:"action_api_url"`
	Auth                 authConfig                 `yaml:"auth"`
	AdminAccess          string                     `yaml:"admin_access"`
	Targets              map[string]delivery.Target `yaml:"targets"`
	Blobs                blob.Limits                `yaml:"blobs"`
	SecretKeyFile        string                     `yaml:"secret_key_file"`
	Retention            retentionConfig            `yaml:"retention"`
	DiskSpace            server.DiskSpace           `yaml:"disk_space"`
	Listen               string                     `yaml:"listen"`
	DataDir              string                     `yaml:"data_dir"`
	Namespace            string                     `yaml:"namespace"`
	PublicURL            string                     `yaml:"public_url"`
	InternalURL          string                     `yaml:"internal_url"`
	MaxParallel          int                        `yaml:"max_parallel"`
	Repositories         []string                   `yaml:"repositories"`
	Workflows            []string                   `yaml:"workflows"`
	PackageTokens        map[string]string          `yaml:"package_tokens"`
	RepositoryTokens     server.RepositoryTokens    `yaml:"repository_tokens"`
	Profiles             map[string]kube.Profile    `yaml:"profiles"`
	Kubeconfig           string                     `yaml:"kubeconfig"`
	GitHubAPIURL         string                     `yaml:"github_api_url"`
	AppID                string                     `yaml:"github_app_id"`
	AppKeyFile           string                     `yaml:"github_app_key_file"`
	TLSCertFile          string                     `yaml:"tls_cert_file"`
	TLSKeyFile           string                     `yaml:"tls_key_file"`
	CASecret             string                     `yaml:"runtime_ca_secret"`
	AllowInsecureRuntime bool                       `yaml:"allow_insecure_runtime"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runCommand(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("forgelet stopped", "error", err)
		os.Exit(1)
	}
}

func runCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("forgelet", flag.ContinueOnError)
	path := flags.String("config", "forgelet.yaml", "path to server configuration")
	restorePath := flags.String("prepare-restore", "", "quarantine unfinished work in a backup database, then exit; stop the old server first")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	explicitConfig, explicitRestore := false, false
	flags.Visit(func(f *flag.Flag) {
		explicitConfig = explicitConfig || f.Name == "config"
		explicitRestore = explicitRestore || f.Name == "prepare-restore"
	})
	if explicitRestore {
		if *restorePath == "" || explicitConfig {
			return errors.New("prepare-restore requires a database path and cannot be combined with config")
		}
		if err := storage.PrepareRestore(ctx, *restorePath); err != nil {
			return fmt.Errorf("prepare restore: %w", err)
		}
		slog.Info("restore prepared; unfinished work quarantined, server not started", "database", *restorePath)
		return nil
	}
	return serve(ctx, *path)
}

func loadConfig(path string) (config, error) {
	f, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("open config: %w", err)
	}
	const maxConfigBytes = 1 << 20
	contents, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err := errors.Join(err, f.Close()); err != nil {
		return config{}, fmt.Errorf("read config: %w", err)
	}
	if len(contents) > maxConfigBytes {
		return config{}, errors.New("config exceeds 1 MiB")
	}
	// yaml.v3 permits float-to-int coercion; a concurrency budget must be an
	// explicit integer, not a truncated floating-point value or null default.
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return config{}, fmt.Errorf("decode config syntax: %w", err)
	}
	if len(document.Content) == 1 && document.Content[0].Kind == yaml.MappingNode {
		fields := document.Content[0].Content
		for i := 0; i+1 < len(fields); i += 2 {
			if fields[i].Value == "max_parallel" && (fields[i+1].Kind != yaml.ScalarNode || fields[i+1].Tag != "!!int") {
				return config{}, errors.New("max_parallel must be an integer")
			}
			if fields[i].Value == "disk_space" {
				if err := validateDiskSpaceYAML(fields[i+1]); err != nil {
					return config{}, err
				}
			}
		}
	}
	cfg := config{Listen: ":8080", DataDir: "./data", Namespace: "forgelet", MaxParallel: 4, Retention: defaultRetentionConfig(), DiskSpace: server.DefaultDiskSpace(), Blobs: blob.DefaultLimits()}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return config{}, errors.New("config must contain exactly one YAML document")
	}
	if err := validateConfig(cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func validateConfig(cfg config) error {
	if cfg.AdminAccess != "" && cfg.AdminAccess != "token" && cfg.AdminAccess != "open" && cfg.AdminAccess != "session" {
		return errors.New("admin_access must be open, token or session")
	}
	if err := cfg.DiskSpace.Validate(); err != nil {
		return err
	}
	if cfg.Blobs != (blob.Limits{}) {
		if err := cfg.Blobs.Validate(); err != nil {
			return err
		}
	}
	for name, target := range cfg.Targets {
		target.Name = name
		if err := target.Validate(); err != nil {
			return fmt.Errorf("target %s: %w", name, err)
		}
	}
	if _, err := cfg.Retention.policy(); err != nil {
		return err
	}
	if cfg.MaxParallel < 1 || cfg.MaxParallel > 1024 {
		return errors.New("max_parallel must be between 1 and 1024")
	}
	if strings.TrimSpace(cfg.DataDir) == "" || strings.TrimSpace(cfg.Namespace) == "" {
		return errors.New("data_dir and namespace are required")
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return errors.New("listen must use host:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("listen port must be between 0 and 65535")
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return errors.New("tls_cert_file and tls_key_file must be configured together")
	}
	if (cfg.AppID == "") != (cfg.AppKeyFile == "") {
		return errors.New("github_app_id and github_app_key_file must be configured together")
	}
	if cfg.InternalURL == "" {
		return errors.New("internal_url is required")
	}
	u, err := url.Parse(cfg.InternalURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("internal_url must be an absolute URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !cfg.AllowInsecureRuntime) {
		return errors.New("internal_url requires HTTPS; HTTP requires allow_insecure_runtime: true")
	}
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("public_url must be an absolute HTTP(S) URL without credentials, query or fragment")
		}
	}
	if len(cfg.Profiles) == 0 {
		return errors.New("at least one execution profile is required")
	}
	for name, profile := range cfg.Profiles {
		if strings.TrimSpace(name) == "" {
			return errors.New("execution profile name must not be empty")
		}
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("profile %s: %w", name, err)
		}
	}
	return nil
}

func serve(ctx context.Context, path string) error {
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	adminToken := os.Getenv("FORGELET_ADMIN_TOKEN")
	if cfg.AdminAccess != "open" && cfg.AdminAccess != "session" && len(adminToken) < 16 {
		return errors.New("FORGELET_ADMIN_TOKEN must have at least 16 characters")
	}
	var tlsConfig *tls.Config
	if cfg.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load TLS certificate and key: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	var secretKey []byte
	if cfg.SecretKeyFile != "" {
		secretKey, err = os.ReadFile(cfg.SecretKeyFile)
		if err != nil {
			return fmt.Errorf("read secret encryption key: %w", err)
		}
		if len(secretKey) != 32 {
			return errors.New("secret_key_file must contain exactly 32 raw bytes")
		}
	}
	var provider server.Provider
	if cfg.AppID != "" {
		key, err := os.ReadFile(cfg.AppKeyFile)
		if err != nil {
			return fmt.Errorf("read GitHub App key: %w", err)
		}
		provider, err = gh.New(cfg.GitHubAPIURL, cfg.AppID, key, &http.Client{Timeout: 20 * time.Second})
		if err != nil {
			return err
		}
	}
	var rc *rest.Config
	if cfg.Kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return fmt.Errorf("kubernetes configuration: %w", err)
	}
	rc.Timeout = 15 * time.Second
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}
	db, err := storage.Open(ctx, filepath.Join(cfg.DataDir, "forgelet.db"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	blobs, err := blob.Open(filepath.Join(cfg.DataDir, "blobs"), cfg.Blobs)
	if err != nil {
		return err
	}
	runtime := &kube.Runtime{Client: client, Namespace: cfg.Namespace, ServerURL: cfg.InternalURL, AllowInsecure: cfg.AllowInsecureRuntime, CASecret: cfg.CASecret}
	retention, err := cfg.Retention.policy()
	if err != nil {
		return err
	}
	var coordinator *delivery.Coordinator
	if len(cfg.Targets) > 0 || provider != nil {
		repo, ok := provider.(delivery.Repository)
		if !ok {
			return errors.New("GitOps targets require GitHub App configuration")
		}
		dyn, err := dynamic.NewForConfig(rc)
		if err != nil {
			return err
		}
		for name, t := range cfg.Targets {
			t.Name = name
			cfg.Targets[name] = t
		}
		coordinator = &delivery.Coordinator{Store: db, Repository: repo, Observer: &kube.FluxObserver{Dynamic: dyn, Client: client}, Now: time.Now, PollInterval: 30 * time.Second}
	}
	authentication := server.AuthConfig{BootstrapPasswordFile: cfg.Auth.BootstrapPasswordFile}
	if cfg.Auth.GitHubClientID != "" || cfg.Auth.GitHubClientSecretFile != "" {
		secret, err := os.ReadFile(cfg.Auth.GitHubClientSecretFile)
		if err != nil {
			return fmt.Errorf("read GitHub OAuth client secret: %w", err)
		}
		authentication.GitHub, err = gh.NewOAuth(cfg.GitHubAPIURL, cfg.Auth.GitHubWebURL, cfg.Auth.GitHubClientID, strings.TrimSpace(string(secret)), strings.TrimRight(cfg.PublicURL, "/")+"/api/auth/github/callback", nil)
		if err != nil {
			return err
		}
	}
	app, err := server.New(server.Config{PackageTokens: cfg.PackageTokens, GitHubAPIURL: cfg.GitHubAPIURL, ActionResolver: &action.Resolver{APIURL: cfg.ActionAPIURL}, RepositoryTokens: cfg.RepositoryTokens, Auth: authentication, AdminAccess: cfg.AdminAccess, Targets: cfg.Targets, Delivery: coordinator, Blobs: blobs, SecretKey: secretKey, Retention: retention, DiskSpace: cfg.DiskSpace, AdminToken: adminToken, WebhookSecret: os.Getenv("FORGELET_WEBHOOK_SECRET"), PublicURL: cfg.PublicURL, MaxParallel: cfg.MaxParallel, Repositories: cfg.Repositories, Workflows: cfg.Workflows, Profiles: cfg.Profiles, DataDir: cfg.DataDir}, db, runtime, provider)
	if err != nil {
		return err
	}
	if err := app.LoadManagement(ctx); err != nil {
		return err
	}
	if err := app.InitAuth(ctx); err != nil {
		return err
	}
	httpServer := &http.Server{Addr: cfg.Listen, Handler: app.Handler(), TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	// WriteTimeout is intentionally unset for resumable SSE streams.
	stopped := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			stopped <- httpServer.ListenAndServeTLS("", "")
		} else {
			stopped <- httpServer.ListenAndServe()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	slog.Info("forgelet started", "listen", cfg.Listen, "max_parallel", cfg.MaxParallel)
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown HTTP: %w", err)
			}
			return nil
		case err := <-stopped:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("serve HTTP: %w", err)
		case <-ticker.C:
			tickCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := app.Tick(tickCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("reconcile will retry", "error", err)
			}
		}
	}
}
