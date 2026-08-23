package server

import (
	"net/http"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/exec"
)

// rejectReadOnly answers a write on a read-only server with 405 read_only
// before the body is read or parsed (the executor's gate stays as the
// authority for anything that reaches it, e.g. plugin-forced paths). Allow
// lists the route's read methods.
func (h *Handler) rejectReadOnly(w http.ResponseWriter, r *http.Request, m *api.Match) bool {
	if h.Exec == nil || h.Exec.Opts.ReadOnly == nil || !h.Exec.Opts.ReadOnly.Load() || !isMutation(r.Method, m) {
		return false
	}
	h.writeError(w, r, m, &exec.Error{Status: http.StatusMethodNotAllowed, Code: exec.CodeReadOnly,
		Detail: "server is in read-only mode: writes are disabled"})
	return true
}
