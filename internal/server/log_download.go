package server

import (
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/shitamachi/forgelet/internal/storage"
)

// downloadLogs drains every stored page; memory remains bounded by one log page.
func (s *Server) downloadLogs(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(r.Context(), chi.URLParam(r, "jobID"))
	if err != nil {
		serverError(w, err)
		return
	}
	if job.LogsExpired {
		serverError(w, storage.ErrLogsExpired)
		return
	}
	entries, err := s.store.Logs(r.Context(), job.ID, 0)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="forgelet-%s.log"`, job.ID))
	for len(entries) > 0 {
		for _, entry := range entries {
			if _, err := io.WriteString(w, entry.Data); err != nil {
				return
			}
		}
		if err := r.Context().Err(); err != nil {
			return
		}
		entries, err = s.store.Logs(r.Context(), job.ID, entries[len(entries)-1].Seq)
		if err != nil {
			return
		}
	}
}
