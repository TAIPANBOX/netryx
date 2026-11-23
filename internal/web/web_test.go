package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// built is a stand-in for dist/ after web/build.sh: the handler is tested
// through it, because the embedded dist/ is empty on a laptop and full on forge.
func built() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte(`<!doctype html><title>netryx</title><script type="module" src="/app.js"></script>`)},
		"app.js":     {Data: []byte(`console.log("app")`)},
		"style.css":  {Data: []byte(`body{margin:0}`)},
		"assets":     {Mode: fs.ModeDir | 0o755}, // a directory, which must never be listed
	}
}

func get(t *testing.T, h http.Handler, method, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Result()
}

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUnbuiltUIExplainsItself(t *testing.T) {
	h := newHandler(fstest.MapFS{".keep": {Data: nil}})
	for _, p := range []string{"/", "/clients", "/client/aa:bb:cc:dd:ee:01"} {
		r := get(t, h, http.MethodGet, p)
		if r.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503 so a probe does not take the placeholder for the console", p, r.StatusCode)
		}
		b := body(t, r)
		if !strings.Contains(b, "web/build.sh") {
			t.Errorf("%s: the placeholder does not say how to build the UI:\n%s", p, b)
		}
		if strings.Contains(b, "<style") || strings.Contains(b, "style=") || strings.Contains(b, "<script") {
			t.Errorf("%s: the placeholder carries inline style or script, which its own CSP refuses:\n%s", p, b)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type %q, want text/html", p, ct)
		}
	}
	// An asset of a UI that was never built is simply not there.
	if r := get(t, h, http.MethodGet, "/app.js"); r.StatusCode != http.StatusNotFound {
		t.Errorf("/app.js on an unbuilt UI: status %d, want 404", r.StatusCode)
	}
	if built := isBuilt(fstest.MapFS{".keep": {}}); built {
		t.Error("a dist/ holding only .keep reports itself as built")
	}
	if !isBuilt(built()) {
		t.Error("a dist/ with index.html reports itself as unbuilt")
	}
}

func TestUIHeadersForbidOutsideLoads(t *testing.T) {
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	}
	cases := []struct {
		name string
		h    http.Handler
		path string
	}{
		{"the app", newHandler(built()), "/"},
		{"a client route", newHandler(built()), "/flows"},
		{"the bundle", newHandler(built()), "/app.js"},
		{"a missing asset", newHandler(built()), "/nope.js"},
		{"the unbuilt placeholder", newHandler(fstest.MapFS{}), "/"},
		{"a refused method", newHandler(built()), "/"},
	}
	for i, c := range cases {
		method := http.MethodGet
		if i == len(cases)-1 {
			method = http.MethodPost
		}
		r := get(t, c.h, method, c.path)
		for k, v := range want {
			if got := r.Header.Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got, v)
			}
		}
	}
}

func TestClientRoutesServeTheApp(t *testing.T) {
	h := newHandler(built())
	index := string(built()["index.html"].Data)
	for _, p := range []string{"/", "/clients", "/client/aa:bb:cc:dd:ee:01", "/flows", "/index.html"} {
		r := get(t, h, http.MethodGet, p)
		if r.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", p, r.StatusCode)
			continue
		}
		if b := body(t, r); b != index {
			t.Errorf("%s: served %q, want index.html", p, b)
		}
		if cc := r.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache so a new build is picked up", p, cc)
		}
	}

	r := get(t, h, http.MethodGet, "/app.js")
	if r.StatusCode != http.StatusOK || body(t, r) != `console.log("app")` {
		t.Errorf("/app.js: status %d, want the bundle", r.StatusCode)
	}
	if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("/app.js: Content-Type %q, want javascript, or nosniff makes the browser refuse it", ct)
	}
	if ct := get(t, h, http.MethodGet, "/style.css").Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("/style.css: Content-Type %q, want text/css", ct)
	}

	// A path with a dot is a file: a missing one is a 404, not the app, so a
	// broken asset link shows up as broken instead of as HTML parsed as script.
	for _, p := range []string{"/missing.js", "/../web.go", "/assets/../../etc/passwd.txt", "/assets.d"} {
		if r := get(t, h, http.MethodGet, p); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, r.StatusCode)
		}
	}
	// A directory is never listed, even when asked for by a dotted name.
	dirs := built()
	dirs["v1.0"] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
	dirs["v1.0/a.js"] = &fstest.MapFile{Data: []byte("x")}
	if r := get(t, newHandler(dirs), http.MethodGet, "/v1.0"); r.StatusCode != http.StatusNotFound {
		t.Errorf("/v1.0 (a directory): status %d, want 404", r.StatusCode)
	}

	if r := get(t, h, http.MethodHead, "/"); r.StatusCode != http.StatusOK {
		t.Errorf("HEAD /: status %d, want 200", r.StatusCode)
	}
	r = get(t, h, http.MethodPost, "/")
	if r.StatusCode != http.StatusMethodNotAllowed || r.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST /: status %d Allow %q, want 405 with Allow: GET, HEAD", r.StatusCode, r.Header.Get("Allow"))
	}
}

func TestTheEmbeddedUIAgreesWithItself(t *testing.T) {
	// On a laptop dist/ holds only .keep and the placeholder answers; on forge
	// after web/build.sh the real page does. Either way Built() must say which.
	r := get(t, Handler(), http.MethodGet, "/")
	if Built() && r.StatusCode != http.StatusOK {
		t.Errorf("Built() is true but / answered %d", r.StatusCode)
	}
	if !Built() && r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Built() is false but / answered %d", r.StatusCode)
	}
}

// The page's build is the one build.sh wrote beside it; a tree without one
// (go test, a binary built without the page) has none.
func TestTheBuildIsReadFromBesideThePage(t *testing.T) {
	if got := buildOf(fstest.MapFS{"build.txt": {Data: []byte("3f2a9c01b7de\n")}}); got != "3f2a9c01b7de" {
		t.Errorf("build %q", got)
	}
	if got := buildOf(fstest.MapFS{}); got != "" {
		t.Errorf("no build.txt gave %q", got)
	}
	if got := buildOf(fstest.MapFS{"build.txt": {Data: []byte("<script>\n")}}); got != "" {
		t.Errorf("a build that is not a short hex id gave %q", got)
	}
}

// Every API answer says which page the router serves now, so a page left open
// across an upgrade can tell that it is the old one.
func TestEveryAnswerNamesTheBuildServed(t *testing.T) {
	h := WithBuild("3f2a9c01b7de", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/overview", nil))
	if got := rec.Header().Get("X-Netryx-Build"); got != "3f2a9c01b7de" {
		t.Errorf("header %q", got)
	}
	rec = httptest.NewRecorder()
	WithBuild("", http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	if _, ok := rec.Header()["X-Netryx-Build"]; ok {
		t.Errorf("a binary without a page build named one")
	}
}

// The first screen's files are the chunks the page's script imports
// statically, listed by build.sh beside it; each is a clean path under
// /chunks/, and a line that is not one is no file.
func TestTheFirstScreensChunksAreReadFromBesideThePage(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":       {Data: []byte("<html>")},
		"app.js":           {Data: []byte("")},
		"first-screen.txt": {Data: []byte("chunks/chunk-AAAA.js\nchunks/chunk-BBBB.js\n../api/x.js\nchunks/../../etc/passwd\nicons/x.svg\n\n")},
	}
	got := firstScreen(fsys)
	if len(got) != 2 || got[0] != "/chunks/chunk-AAAA.js" || got[1] != "/chunks/chunk-BBBB.js" {
		t.Errorf("first screen %v, want the two chunks alone", got)
	}
	if got := firstScreen(fstest.MapFS{"index.html": {Data: []byte("x")}}); got != nil {
		t.Errorf("without the list: %v", got)
	}
}
