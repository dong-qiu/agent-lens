// Package webui embeds the built Lens UI (web/dist after `make web-build`)
// so the agent-lens binary can serve it directly. v0.1 personal-mode users
// run `docker compose up` and expect a single URL to navigate to (issue #5).
//
// The embed directive points at internal/webui/dist/, which is populated by
// `make embed-webui` (a thin wrapper that copies web/dist/ here after the
// vite build). The dist/ directory is gitignored except for `.gitkeep`,
// so go build always succeeds even before the first UI build — Files just
// resolves to an empty FS in that case and the static handler returns a
// helpful 404 stub at /.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedFS embed.FS

// Files is the embedded UI bundle, sub-rooted at dist/. Use Available()
// to check if real UI assets are present (vs only the .gitkeep stub).
var Files fs.FS

func init() {
	sub, err := fs.Sub(embedFS, "dist")
	if err == nil {
		Files = sub
	}
}

// Available reports whether the embedded UI has a real index.html (i.e.
// `make embed-webui` ran). When false, the static handler should return
// the dev-mode stub instead of letting users see broken file listings.
func Available() bool {
	if Files == nil {
		return false
	}
	_, err := fs.Stat(Files, "index.html")
	return err == nil
}

// Handler returns an http.Handler that serves the embedded UI with SPA
// fallback (any unknown path returns index.html so client-side routing
// works). When the UI isn't built, returns a 503 with a hint.
func Handler() http.Handler {
	if !Available() {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("Lens UI not embedded in this build.\n" +
				"For development: run `make web-dev` for the Vite dev server (proxies /v1 to :8787).\n" +
				"For production: rebuild with `make build` (which runs make web-build && make embed-webui first).\n"))
		})
	}
	fileServer := http.FileServer(http.FS(Files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: any path that doesn't resolve to an embedded
		// file gets index.html so React Router can take over. We only
		// fall back for GET; POST/PUT/DELETE shouldn't hit static.
		if r.Method == http.MethodGet {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}
			if _, err := fs.Stat(Files, path); errors.Is(err, fs.ErrNotExist) {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				fileServer.ServeHTTP(w, r2)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
