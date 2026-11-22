// Package web serves the console's page: the bundle web/build.sh writes into
// dist/, embedded in the binary so the router needs nothing beside it.
package web

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// csp is the whole promise that the page loads nothing from outside the
// router: no CDN, no font service, no inline script, and no inline style, so a
// device name that smuggles markup in cannot style or script the page either.
// frame-ancestors 'none' keeps another page from framing the console to trick
// a click on it.
const csp = "default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'"

// Handler serves the embedded UI. Routes are client-side (#/clients, ...), so
// any path without a dot is the app itself.
func Handler() http.Handler { return newHandler(embedded()) }

// Built reports whether the binary carries a real UI rather than the
// placeholder, so packaging can refuse a binary that would only apologise.
func Built() bool { return isBuilt(embedded()) }

func embedded() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil { // only if the embed directive itself changed
		panic(err)
	}
	return sub
}

func isBuilt(files fs.FS) bool {
	st, err := fs.Stat(files, "index.html")
	return err == nil && !st.IsDir()
}

// unbuilt is what a binary built without web/build.sh shows. It carries no
// style or script, because its own CSP would refuse them.
const unbuilt = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>netryx: console not built</title></head>
<body>
<h1>The netryx console is not built</h1>
<p>This binary was built without the console page. The API works at <a href="/api/overview">/api/overview</a>.</p>
<p>To add the page, build it and rebuild the binary: <code>sh web/build.sh</code>, then <code>go build ./cmd/netryx</code>.</p>
</body>
</html>
`

type handler struct {
	files fs.FS
	index []byte // nil when the UI was not built
}

func newHandler(files fs.FS) http.Handler {
	h := &handler{files: files}
	if isBuilt(files) {
		// The FS is immutable (embedded), so the page is read once.
		if b, err := fs.ReadFile(files, "index.html"); err == nil {
			h.index = b
		}
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", csp)
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hd.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	// File names carry no hash, so every response is revalidated: a new build
	// is seen on the next load instead of after a cache expires.
	hd.Set("Cache-Control", "no-cache")

	if name == "" || name == "index.html" || !strings.Contains(path.Base(name), ".") {
		h.app(w, r)
		return
	}
	h.asset(w, r, name)
}

func (h *handler) app(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.index == nil {
		// 503, not 200: a health probe must not mistake the placeholder for
		// the console.
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method == http.MethodGet {
			io.WriteString(w, unbuilt)
		}
		return
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

// asset serves one file. A missing file is a 404, never the app: a broken
// link to app.js must fail as a 404, not hand the browser HTML as script.
func (h *handler) asset(w http.ResponseWriter, r *http.Request, name string) {
	f, err := h.files.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() { // a directory is never listed
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}
	// ServeContent sets Content-Type from the extension, which nosniff
	// requires to be right for scripts and styles.
	http.ServeContent(w, r, name, time.Time{}, rs)
}

// buildID is what build.sh writes to dist/build.txt: a short hex digest of
// the page's sources.
var buildID = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// Build is the page this binary serves, "" when it carries none.
func Build() string { return buildOf(embedded()) }

func buildOf(files fs.FS) string {
	b, err := fs.ReadFile(files, "build.txt")
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if !buildID.MatchString(id) {
		return ""
	}
	return id
}

// WithBuild has every answer of h name the page build the router serves now
// (X-Netryx-Build), so a page left open across an upgrade can tell it is the
// old one and offer a reload. An empty build names nothing.
func WithBuild(build string, h http.Handler) http.Handler {
	if build == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Netryx-Build", build)
		h.ServeHTTP(w, r)
	})
}
