package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/suprbdev/pdbr/internal/exec"
)

// Problem is an RFC 9457 problem-details body with pdbr's code extension.
type Problem struct {
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Status   int            `json:"status"`
	Detail   string         `json:"detail,omitempty"`
	Instance string         `json:"instance,omitempty"`
	Code     string         `json:"code,omitempty"`
	PG       map[string]any `json:"pg,omitempty"`
}

// Stable pdbr error codes rendered by the server itself.
const (
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeInvalidToken     = "invalid_token"
	CodeRoleNotAllowed   = "role_not_allowed"
	CodeBodyTooLarge     = "body_too_large"
	CodeTimeout          = "timeout"
	CodeInternal         = "internal"
	CodeNotImplemented   = "not_implemented"
	CodeUnavailable      = exec.CodeUnavailable
)

// RetryAfterUnavailable is the Retry-After value (seconds) on every 503.
const RetryAfterUnavailable = "1"

// ContentTypeProblem is the media type of every error response.
const ContentTypeProblem = "application/problem+json"

// WriteProblem renders a problem+json response. instance is the request
// path without the query string; HEAD requests get headers only.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	WriteProblemPG(w, r, status, code, detail, nil)
}

// WriteProblemPG is WriteProblem with the errors.detail=dev pg object.
func WriteProblemPG(w http.ResponseWriter, r *http.Request, status int, code, detail string, pg map[string]any) {
	p := Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
		Code:   code,
		PG:     pg,
	}
	if r != nil {
		p.Instance = r.URL.Path
	}
	w.Header().Set("Content-Type", ContentTypeProblem)
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", RetryAfterUnavailable)
	}
	w.WriteHeader(status)
	if r != nil && r.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(w).Encode(p); err != nil {
		slog.Debug("write problem", "err", err)
	}
}
