package action

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/shitamachi/forgelet/internal/run"
)

// ErrUnavailable marks failures for which event ingestion should retry rather
// than permanently rejecting a workflow (network, rate limit, GitHub outage).
var ErrUnavailable = errors.New("public Action source temporarily unavailable")

// Resolver only reads public GitHub sources. It never receives an installation,
// OAuth or registry token. Each call snapshots refs anew; reruns use saved plans.
type Resolver struct {
	Client     *http.Client
	APIURL     string
	ArchiveURL string
	RawURL     string
}
type resolution struct {
	resolver Resolver
	pins     map[string]string
	metadata map[string]bool
	active   map[string]bool
	bytes    int
}

// Resolve mutates newly compiled act plans before persistence. Composite remote
// dependencies share the same bounded lock; no mutable lookup happens in a job.
func (r Resolver) Resolve(ctx context.Context, plans []run.Plan) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	state := resolution{resolver: r, pins: map[string]string{}, metadata: map[string]bool{}, active: map[string]bool{}}
	for i := range plans {
		p := &plans[i]
		if p.Kernel != "act" {
			continue
		}
		for j := range p.Steps {
			step := &p.Steps[j]
			uses := ""
			if step.Checkout != nil {
				uses = "actions/checkout@v4"
			}
			if step.Action != nil && !strings.HasPrefix(step.Action.Uses, "./") {
				uses = step.Action.Uses
			}
			if uses == "" {
				continue
			}
			a, err := RemoteReference(uses)
			if err != nil {
				return err
			}
			if err := state.resolve(ctx, a, 0); err != nil {
				return fmt.Errorf("resolve Action %s: %w", uses, err)
			}
			if step.Action != nil {
				step.Action.Repository, step.Action.Ref, step.Action.SHA, step.Action.Path = a.Repository, a.Ref, a.SHA, a.Path
			}
		}
	}
	for i := range plans {
		if plans[i].Kernel == "act" {
			plans[i].ActionPins = maps.Clone(state.pins)
		}
	}
	return nil
}
func (s *resolution) resolve(ctx context.Context, a *run.Action, depth int) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if depth > 8 {
		return errors.New("action dependency nesting exceeds 8")
	}
	key := a.Repository + "@" + a.Ref
	if pinned := s.pins[key]; pinned != "" {
		a.SHA = pinned
	} else {
		if len(s.pins) >= 64 {
			return errors.New("action source lock exceeds 64 references")
		}
		if a.SHA == "" {
			sha, err := s.commit(ctx, a.Repository, a.Ref)
			if err != nil {
				return err
			}
			a.SHA = sha
		}
		s.pins[key] = a.SHA
	}
	identity := a.Repository + "/" + a.Path + "@" + a.SHA
	if s.active[identity] {
		return errors.New("cyclic Composite Action dependency")
	}
	if s.metadata[identity] {
		return nil
	}
	if len(s.metadata)+len(s.active) >= 64 {
		return errors.New("action metadata graph exceeds 64 sources")
	}
	s.active[identity] = true
	defer delete(s.active, identity)
	var document struct {
		Runs struct {
			Using string `yaml:"using"`
			Steps []struct {
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	var data []byte
	for _, filename := range []string{"action.yml", "action.yaml"} {
		parts := strings.Split(path.Join(a.Path, filename), "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		endpoint := "/repos/" + a.Repository + "/contents/" + strings.Join(parts, "/") + "?ref=" + a.SHA
		var body []byte
		var status int
		var err error
		if s.resolver.APIURL == "" {
			base := s.resolver.RawURL
			if base == "" {
				base = "https://raw.githubusercontent.com"
			}
			body, status, err = s.getFrom(ctx, base, "/"+a.Repository+"/"+a.SHA+"/"+strings.Join(parts, "/"), "application/octet-stream")
		} else {
			body, status, err = s.get(ctx, endpoint, "application/vnd.github.raw+json")
		}
		if err != nil {
			return err
		}
		if status == http.StatusNotFound {
			continue
		}
		data = body
		break
	}
	if data == nil {
		return errors.New("public Action metadata not found at fixed commit (private Actions are unsupported)")
	}
	s.bytes += len(data)
	if s.bytes > 4<<20 {
		return errors.New("action metadata graph exceeds 4 MiB")
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse Action metadata: %w", err)
	}
	switch document.Runs.Using {
	case "node20", "node24", "docker":
	case "composite":
		for _, step := range document.Runs.Steps {
			if step.Uses == "" || strings.HasPrefix(step.Uses, "./") || strings.HasPrefix(step.Uses, "docker://") {
				continue
			}
			child, err := RemoteReference(step.Uses)
			if err != nil {
				return err
			}
			if err := s.resolve(ctx, child, depth+1); err != nil {
				return fmt.Errorf("composite dependency %s: %w", step.Uses, err)
			}
		}
	default:
		return fmt.Errorf("unsupported Action runtime %q", document.Runs.Using)
	}
	s.metadata[identity] = true
	return nil
}
func (s *resolution) commit(ctx context.Context, repository, ref string) (string, error) {
	if s.resolver.APIURL == "" {
		return s.archiveCommit(ctx, repository, ref)
	}
	var object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	found := false
	// GitHub resolves an exact tag before an equally named branch. Never use a
	// prefix match (matching-refs) or accept a shortened commit as a fixed SHA.
	for _, namespace := range []string{"tags", "heads"} {
		data, status, err := s.get(ctx, "/repos/"+repository+"/git/ref/"+namespace+"/"+url.PathEscape(ref), "application/vnd.github+json")
		if err != nil {
			return "", err
		}
		if status == http.StatusNotFound {
			continue
		}
		var response struct {
			Object struct {
				Type string `json:"type"`
				SHA  string `json:"sha"`
			} `json:"object"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return "", fmt.Errorf("decode Action ref: %w", err)
		}
		object = response.Object
		found = true
		break
	}
	if !found {
		return "", errors.New("public Action tag/branch not found (private Actions are unsupported)")
	}
	for depth := 0; depth < 8; depth++ {
		if !shaPattern.MatchString(object.SHA) {
			return "", errors.New("action ref did not return a full commit SHA")
		}
		switch object.Type {
		case "commit":
			return strings.ToLower(object.SHA), nil
		case "tag":
			data, _, err := s.get(ctx, "/repos/"+repository+"/git/tags/"+object.SHA, "application/vnd.github+json")
			if err != nil {
				return "", err
			}
			var response struct {
				Object struct {
					Type string `json:"type"`
					SHA  string `json:"sha"`
				} `json:"object"`
			}
			if err := json.Unmarshal(data, &response); err != nil {
				return "", fmt.Errorf("decode annotated Action tag: %w", err)
			}
			object = response.Object
		default:
			return "", fmt.Errorf("action ref targets %q instead of a commit", object.Type)
		}
	}
	return "", errors.New("annotated Action tag nesting exceeds 8")
}
func (s *resolution) get(ctx context.Context, endpoint, accept string) ([]byte, int, error) {
	base := s.resolver.APIURL
	if base == "" {
		base = "https://api.github.com"
	}
	return s.getFrom(ctx, base, endpoint, accept)
}
func (s *resolution) getFrom(ctx context.Context, base, endpoint, accept string) ([]byte, int, error) {
	response, err := s.open(ctx, base, endpoint, accept)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, response.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil {
		return nil, response.StatusCode, fmt.Errorf("%w: read public Action response: %w", ErrUnavailable, err)
	}
	if len(data) > 256<<10 {
		return nil, response.StatusCode, errors.New("public Action response exceeds 256 KiB")
	}
	return data, response.StatusCode, nil
}
func (s *resolution) open(ctx context.Context, base, endpoint, accept string) (*http.Response, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, errors.New("invalid public Action API URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("public Action request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "forgelet-action-resolver")
	client := http.Client{Timeout: 20 * time.Second}
	if s.resolver.Client != nil {
		client = *s.resolver.Client
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	code := response.StatusCode
	if code == http.StatusOK || code == http.StatusNotFound {
		return response, nil
	}
	_ = response.Body.Close()
	if code == 403 || code == 429 || code >= 500 {
		return nil, fmt.Errorf("%w: GitHub HTTP %d (rate limit or source service; retry later)", ErrUnavailable, code)
	}
	return nil, fmt.Errorf("public Action source returned HTTP %d", code)
}

// Git archive embeds its commit ID in a global PAX comment. Read only the
// bounded header, close the mutable stream, then fetch metadata/archive by SHA.
// This avoids the anonymous REST quota without accepting a mutable execution ref.
func (s *resolution) archiveCommit(ctx context.Context, repository, ref string) (string, error) {
	base := s.resolver.ArchiveURL
	if base == "" {
		base = "https://codeload.github.com"
	}
	for _, namespace := range []string{"tags", "heads"} {
		response, err := s.openArchive(ctx, base, "/"+repository+"/tar.gz/refs/"+namespace+"/"+url.PathEscape(ref))
		if err != nil {
			return "", err
		}
		if response.StatusCode == http.StatusNotFound {
			_ = response.Body.Close()
			continue
		}
		sha, err := archiveCommitHeader(response.Body)
		_ = response.Body.Close()
		if err != nil {
			var network net.Error
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &network) {
				return "", fmt.Errorf("%w: read Git archive header: %w", ErrUnavailable, err)
			}
			return "", fmt.Errorf("read public Git archive commit: %w", err)
		}
		return sha, nil
	}
	return "", errors.New("public Action tag/branch not found (private Actions are unsupported)")
}
func archiveCommitHeader(body io.Reader) (string, error) {
	gz, err := gzip.NewReader(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(io.LimitReader(gz, 1<<20))
	header, err := reader.Next()
	if err != nil {
		return "", err
	}
	if header.Typeflag != tar.TypeXGlobalHeader || !shaPattern.MatchString(header.PAXRecords["comment"]) {
		return "", errors.New("git archive has no full commit ID in global PAX header")
	}
	return strings.ToLower(header.PAXRecords["comment"]), nil
}

// Reading only a Git archive header deliberately cancels a large HTTP/2 stream.
// Give that stream its own connection so a slow cancelled response cannot stall
// later refs on a pooled connection. Small, fully-read metadata stays pooled.
func (s *resolution) openArchive(ctx context.Context, base, endpoint string) (*http.Response, error) {
	copy := *s
	client := http.Client{Timeout: 20 * time.Second}
	if s.resolver.Client != nil {
		client = *s.resolver.Client
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if original, ok := transport.(*http.Transport); ok {
		fresh := original.Clone()
		fresh.DisableKeepAlives = true
		client.Transport = fresh
	}
	copy.resolver.Client = &client
	return copy.open(ctx, base, endpoint, "application/octet-stream")
}
