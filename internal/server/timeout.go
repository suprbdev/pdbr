package server

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"
)

// withTimeout bounds every request by d: the response is buffered and
// released when the handler returns; when d elapses first the client gets a
// 504 problem (code timeout) and later writes from the handler are dropped.
// Same shape as http.TimeoutHandler, which cannot render our status/body.
func withTimeout(next http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		tw := &timeoutWriter{h: http.Header{}, code: http.StatusOK}
		done := make(chan struct{})
		panicChan := make(chan any, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					panicChan <- p
				}
			}()
			next.ServeHTTP(tw, r.WithContext(ctx))
			close(done)
		}()
		select {
		case p := <-panicChan:
			panic(p)
		case <-done:
			tw.mu.Lock()
			defer tw.mu.Unlock()
			dst := w.Header()
			for k, v := range tw.h {
				dst[k] = v
			}
			w.WriteHeader(tw.code)
			w.Write(tw.buf.Bytes()) //nolint:errcheck
		case <-ctx.Done():
			tw.mu.Lock()
			defer tw.mu.Unlock()
			tw.timedOut = true
			WriteProblem(w, r, http.StatusGatewayTimeout, CodeTimeout, "request timed out")
		}
	})
}

type timeoutWriter struct {
	mu          sync.Mutex
	h           http.Header
	buf         bytes.Buffer
	code        int
	wroteHeader bool
	timedOut    bool
}

func (tw *timeoutWriter) Header() http.Header { return tw.h }

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut || tw.wroteHeader {
		return
	}
	tw.wroteHeader = true
	tw.code = code
}

func (tw *timeoutWriter) Write(p []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return 0, http.ErrHandlerTimeout
	}
	tw.wroteHeader = true
	return tw.buf.Write(p)
}
