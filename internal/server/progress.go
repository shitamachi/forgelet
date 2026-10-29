package server

import (
	"net/http"

	"github.com/shitamachi/forgelet/internal/run"
)

func (s *Server) appendProgress(w http.ResponseWriter, r *http.Request) {
	job, who, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	var e run.ProgressEvent
	if err := decode(w, r, &e, 8<<10); err != nil {
		http.Error(w, "invalid progress event", http.StatusBadRequest)
		return
	}
	err = s.store.AppendProgress(r.Context(), job.ID, who.PodUID, e)
	respond(w, map[string]int64{"seq": e.Seq}, err)
}
