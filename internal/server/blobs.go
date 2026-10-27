package server

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/run"
)

var blobVersion = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Server) runtimeBlob(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
	job, _, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	if job.State != run.Running || job.CancelRequested {
		http.Error(w, "job is not running", http.StatusConflict)
		return
	}
	if s.cfg.Blobs == nil {
		http.Error(w, "blob storage unavailable", http.StatusServiceUnavailable)
		return
	}
	kind := chi.URLParam(r, "kind")
	q := r.URL.Query()
	key := q.Get("key")
	if kind != "artifact" && kind != "cache" || len(key) == 0 || len(key) > 512 {
		http.Error(w, "invalid blob key", http.StatusBadRequest)
		return
	}
	scope := blob.Scope{Repository: job.Plan.Repository, Kind: kind, Namespace: job.RunID}
	ttl := 7 * 24 * time.Hour
	if kind == "cache" {
		scope.Namespace = job.Plan.Ref
		if scope.Namespace == "" {
			http.Error(w, "cache requires a ref", http.StatusBadRequest)
			return
		}
		scope.Version = q.Get("version")
		if !blobVersion.MatchString(scope.Version) {
			http.Error(w, "cache version required", http.StatusBadRequest)
			return
		}
	} else if q.Get("days") != "" {
		days, err := strconv.Atoi(q.Get("days"))
		if err != nil || days < 1 || days > 90 {
			http.Error(w, "retention-days must be 1..90", http.StatusBadRequest)
			return
		}
		ttl = time.Duration(days) * 24 * time.Hour
	}
	if r.Method == http.MethodPut {
		e, err := s.cfg.Blobs.Put(r.Context(), scope, key, job.ID, ttl, s.now(), http.MaxBytesReader(w, r.Body, s.cfg.Blobs.ObjectLimit(kind)+1))
		if err != nil {
			blobError(w, err)
			return
		}
		writeJSON(w, 200, e)
		return
	}
	prefixes := q["restore-key"]
	if len(prefixes) > 32 {
		http.Error(w, "too many restore keys", http.StatusBadRequest)
		return
	}
	for _, prefix := range prefixes {
		if len(prefix) == 0 || len(prefix) > 512 {
			http.Error(w, "invalid restore key", http.StatusBadRequest)
			return
		}
	}
	if kind != "cache" && len(prefixes) > 0 {
		http.Error(w, "artifact prefix matching unsupported", http.StatusBadRequest)
		return
	}
	e, f, err := s.cfg.Blobs.Get(scope, key, prefixes, s.now())
	if err != nil {
		blobError(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	if q.Get("lookup") == "true" {
		writeJSON(w, 200, e)
		return
	}
	w.Header().Set("X-Forgelet-Key", e.Key)
	w.Header().Set("X-Forgelet-Digest", e.Digest)
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	_, _ = io.Copy(w, f)
}
func blobError(w http.ResponseWriter, err error) {
	status := 500
	switch {
	case errors.Is(err, blob.ErrNotFound):
		status = 404
	case errors.Is(err, blob.ErrConflict):
		status = 409
	case errors.Is(err, blob.ErrLimit):
		status = 413
	}
	http.Error(w, http.StatusText(status), status)
}
func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetRun(r.Context(), chi.URLParam(r, "runID"))
	if err != nil {
		serverError(w, err)
		return
	}
	if s.cfg.Blobs == nil {
		http.Error(w, "blob storage unavailable", http.StatusServiceUnavailable)
		return
	}
	scope := blob.Scope{Repository: item.Repository, Kind: "artifact", Namespace: item.ID}
	name := chi.URLParam(r, "name")
	if name == "" {
		writeJSON(w, 200, s.cfg.Blobs.List(scope, s.now()))
		return
	}
	e, f, err := s.cfg.Blobs.Get(scope, name, nil, s.now())
	if err != nil {
		blobError(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	_, _ = io.Copy(w, f)
}
