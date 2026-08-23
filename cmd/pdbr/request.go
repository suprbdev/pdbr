package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"

	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/server"
)

type requestSpec struct {
	method, path, body string
	headers            []string
	include, printSQL  bool
}

// runRequest runs one request in-process through the real handler (or only
// through routing/parse/compile with printSQL) and prints the result: the
// status line to stderr, headers (with include) and body to stdout. It
// returns *exitError{2} for 4xx/5xx responses; other errors exit 1.
func runRequest(ctx context.Context, cfg config.Config, spec requestSpec, stdout, stderr io.Writer) error {
	req, err := buildRequest(ctx, cfg, spec)
	if err != nil {
		return err
	}
	app := newApp(cfg)
	defer app.Close()

	if spec.printSQL {
		out, err := app.CompileSQL(ctx, req)
		if err != nil {
			var xe *exec.Error
			if !errors.As(err, &xe) {
				return err
			}
			p := server.Problem{Type: "about:blank", Title: http.StatusText(xe.Status), Status: xe.Status,
				Detail: xe.Detail, Instance: req.URL.Path, Code: xe.Code, PG: xe.PG}
			statusLine(stderr, xe.Status)
			b, _ := json.Marshal(p)
			_, _ = fmt.Fprintln(stdout, string(b))
			return &exitError{code: 2}
		}
		_, err = io.WriteString(stdout, out)
		return err
	}

	h, err := app.Handler(ctx)
	if err != nil {
		return err
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	statusLine(stderr, res.StatusCode)
	if spec.include {
		keys := make([]string, 0, len(res.Header))
		for k := range res.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range res.Header[k] {
				if _, err := fmt.Fprintf(stdout, "%s: %s\n", k, v); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return err
		}
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		if _, err := stdout.Write(body); err != nil {
			return err
		}
		if body[len(body)-1] != '\n' {
			_, _ = fmt.Fprintln(stdout)
		}
	}
	if res.StatusCode >= 400 {
		return &exitError{code: 2}
	}
	return nil
}

func statusLine(w io.Writer, status int) {
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\n", status, http.StatusText(status))
}

// buildRequest assembles the http.Request: PATH may omit server.base_path
// (health endpoints live outside it), --body is inline JSON, @file or -
// (stdin), --header lines are 'K: V'.
func buildRequest(ctx context.Context, cfg config.Config, spec requestSpec) (*http.Request, error) {
	path := spec.path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base := cfg.Server.BasePath
	switch {
	case base == "", path == "/healthz", path == "/readyz",
		path == base, strings.HasPrefix(path, base+"/"), strings.HasPrefix(path, base+"?"):
	default:
		path = base + path
	}
	var body io.Reader
	if spec.body != "" {
		var data []byte
		var err error
		switch {
		case spec.body == "-":
			data, err = io.ReadAll(os.Stdin)
		case strings.HasPrefix(spec.body, "@"):
			data, err = os.ReadFile(spec.body[1:])
		default:
			data = []byte(spec.body)
		}
		if err != nil {
			return nil, fmt.Errorf("--body: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, "http://pdbr"+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range spec.headers {
		k, v, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--header %q: want 'Name: value'", h)
		}
		req.Header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	req.RemoteAddr = "127.0.0.1:0"
	return req, nil
}
