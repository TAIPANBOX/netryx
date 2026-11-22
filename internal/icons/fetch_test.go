package icons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// PNG, GIF and JPEG bodies are real 16 px images, because the fetcher reads
// their headers for the size; ICO and WebP are judged by magic and length
// only, so their magic and a little filler are enough.
var (
	pngBody   = encode("png", 16, 16)
	icoBody   = append([]byte("\x00\x00\x01\x00"), bytes.Repeat([]byte{7}, 40)...)
	jpgBody   = encode("jpeg", 16, 16)
	gif89Body = encode("gif", 16, 16)
	gif87Body = append([]byte("GIF87a"), gif89Body[6:]...)
	webpBody  = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{7}, 40)...)
	htmlBody  = []byte("<!doctype html><title>Moved</title><a href=/>home</a>")
	svgBody   = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
)

const maxBytes = 65536

func encode(format string, w, h int) []byte {
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var b bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&b, img)
	case "gif":
		err = gif.Encode(&b, img, nil)
	case "jpeg":
		err = jpeg.Encode(&b, img, nil)
	}
	if err != nil {
		panic(err)
	}
	return b.Bytes()
}

// padded is b followed by filler up to n bytes: a decoder reads the header
// and never looks at what follows the image.
func padded(b []byte, n int) []byte {
	return append(append([]byte{}, b...), bytes.Repeat([]byte{1}, n-len(b))...)
}

// world is the internet as the fetcher sees it in a test: every host name
// resolves to one of two local servers, port 80 to a plain one and anything
// else to a TLS one, so redirects to http://, to other hosts and to look-alike
// names all really happen and the fetcher's own rules are what stop them.
type world struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]http.HandlerFunc // "https://host/path"
	hits   []string
	now    time.Time
	logs   []string
	f      *Fetcher
}

func newWorld(t *testing.T, routes map[string]http.HandlerFunc) *world {
	t.Helper()
	w := &world{t: t, routes: routes, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	h := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		key := scheme + "://" + host + r.URL.Path
		w.mu.Lock()
		w.hits = append(w.hits, key)
		fn := w.routes[key]
		w.mu.Unlock()
		if fn == nil {
			http.NotFound(rw, r)
			return
		}
		fn(rw, r)
	})
	tlsSrv := httptest.NewTLSServer(h)
	t.Cleanup(tlsSrv.Close)
	plain := httptest.NewServer(h)
	t.Cleanup(plain.Close)

	tr := tlsSrv.Client().Transport.(*http.Transport).Clone()
	// The test certificate names example.com; pinning the name keeps real
	// certificate verification on while every host dials the same server.
	tr.TLSClientConfig.ServerName = "example.com"
	var d net.Dialer
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if port == "80" {
			return d.DialContext(ctx, network, plain.Listener.Addr().String())
		}
		return d.DialContext(ctx, network, tlsSrv.Listener.Addr().String())
	}
	w.f = &Fetcher{
		Dir:      filepath.Join(t.TempDir(), "icons"),
		Client:   &http.Client{Transport: tr, Timeout: 5 * time.Second},
		MaxBytes: maxBytes,
		Retry:    7 * 24 * time.Hour,
		Now:      func() time.Time { return w.now },
		Logf: func(format string, args ...any) {
			w.mu.Lock()
			w.logs = append(w.logs, fmt.Sprintf(format, args...))
			w.mu.Unlock()
		},
	}
	return w
}

// clone is the same fetcher as a restarted router would build it: same
// settings and directory, nothing carried over in memory.
func (w *world) clone() *Fetcher {
	return &Fetcher{Dir: w.f.Dir, Client: w.f.Client, MaxBytes: w.f.MaxBytes,
		Retry: w.f.Retry, Now: w.f.Now, Logf: w.f.Logf}
}

func (w *world) hitCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.hits)
}

func serve(contentType string, body []byte) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", contentType)
		rw.Write(body)
	}
}

// serveSized announces its length up front, so the fetcher can refuse before
// reading a byte, or not.
func serveSized(contentType string, body []byte) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", contentType)
		rw.Header().Set("Content-Length", strconv.Itoa(len(body)))
		rw.Write(body)
	}
}

// serveChunked hides its length, so only the reader's own limit stops it.
// Each request walks its own slice header: one handler serves both URLs, and
// a request the client abandoned may still be writing when the next arrives.
func serveChunked(contentType string, body []byte) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", contentType)
		fl := rw.(http.Flusher)
		rest := body
		for len(rest) > 0 {
			n := min(4096, len(rest))
			if _, err := rw.Write(rest[:n]); err != nil {
				return
			}
			fl.Flush()
			rest = rest[n:]
		}
	}
}

func redirect(to string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, to, http.StatusFound)
	}
}

func failures(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "failures.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int64{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("failures.json is not JSON: %v: %q", err, b)
	}
	return m
}

// names lists the directory, so a test can see that nothing but the expected
// files was left behind: no temp file, no half-written icon.
func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestAFaviconIsFetchedOnceAndKept(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	ctx := context.Background()
	if err := w.f.Ensure(ctx, "brand", "brand.test"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if n := w.hitCount(); n != 1 {
		t.Fatalf("first Ensure made %d requests, want 1", n)
	}

	fi, err := os.Stat(w.f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("icon dir mode %o, want 700", perm)
	}
	fi, err = os.Stat(filepath.Join(w.f.Dir, "brand.png"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("icon file mode %o, want 600", perm)
	}
	if got := names(t, w.f.Dir); len(got) != 1 || got[0] != "brand.png" {
		t.Errorf("dir holds %v, want only brand.png", got)
	}

	r, ct, mod, ok := w.f.Open("brand")
	if !ok {
		t.Fatal("Open after a successful Ensure found nothing")
	}
	if ct != "image/png" {
		t.Errorf("content type %q, want image/png", ct)
	}
	if !mod.Equal(fi.ModTime()) {
		t.Errorf("mod time %v, want the file's %v", mod, fi.ModTime())
	}
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, pngBody) {
		t.Errorf("Open returned %q, want the fetched bytes", got)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		t.Errorf("the reader cannot seek: %v", err)
	}

	// Kept: a second call, and a restarted router with a fresh Fetcher on the
	// same directory, a month later, touch the network no more.
	if err := w.f.Ensure(ctx, "brand", "brand.test"); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	w.now = w.now.Add(30 * 24 * time.Hour)
	again := w.clone()
	if err := again.Ensure(ctx, "brand", "brand.test"); err != nil {
		t.Fatalf("Ensure after a restart: %v", err)
	}
	if n := w.hitCount(); n != 1 {
		t.Errorf("a kept icon was fetched again: %d requests in all", n)
	}
	if m := failures(t, w.f.Dir); len(m) != 0 {
		t.Errorf("a success left failures behind: %v", m)
	}
}

func TestAFaviconMustBeASmallRasterImageFromTheBrandsOwnSite(t *testing.T) {
	const touch = "https://brand.test/apple-touch-icon.png"
	const fav = "https://brand.test/favicon.ico"
	exact := padded(pngBody, maxBytes)
	over := padded(pngBody, maxBytes+1)
	huge := padded(pngBody, 10<<20)

	// both makes the two URLs behave alike, so a refusal cannot be rescued by
	// the second one.
	both := func(h http.HandlerFunc) map[string]http.HandlerFunc {
		return map[string]http.HandlerFunc{touch: h, fav: h}
	}
	with := func(m map[string]http.HandlerFunc, extra map[string]http.HandlerFunc) map[string]http.HandlerFunc {
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	cases := []struct {
		name    string
		routes  map[string]http.HandlerFunc
		wantExt string // "" means refused
		wantCT  string
		body    []byte
	}{
		{name: "a redirect to http is refused",
			routes: with(both(redirect("http://brand.test/i.png")), map[string]http.HandlerFunc{
				"http://brand.test/i.png": serve("image/png", pngBody)})},
		{name: "a redirect to another site is refused",
			routes: with(both(redirect("https://other.test/i.png")), map[string]http.HandlerFunc{
				"https://other.test/i.png": serve("image/png", pngBody)})},
		{name: "a redirect to a look-alike name ending in the brand's is refused",
			routes: with(both(redirect("https://evilbrand.test/i.png")), map[string]http.HandlerFunc{
				"https://evilbrand.test/i.png": serve("image/png", pngBody)})},
		{name: "a redirect that hides another host behind user info is refused",
			routes: with(both(redirect("https://brand.test@other.test/i.png")), map[string]http.HandlerFunc{
				"https://other.test/i.png": serve("image/png", pngBody)})},
		{name: "a redirect that carries credentials to the brand's own host is refused",
			routes: with(both(redirect("https://user:pass@brand.test/i.png")), map[string]http.HandlerFunc{
				"https://brand.test/i.png": serve("image/png", pngBody)})},
		{name: "a redirect to an odd port on the brand's own host is refused",
			routes: with(both(redirect("https://brand.test:8443/i.png")), map[string]http.HandlerFunc{
				"https://brand.test/i.png": serve("image/png", pngBody)})},
		{name: "a redirect to the brand's own subdomain over https is followed",
			routes: map[string]http.HandlerFunc{
				touch:                          redirect("https://cdn.brand.test/i.png"),
				"https://cdn.brand.test/i.png": serve("image/png", pngBody)},
			wantExt: "png", wantCT: "image/png", body: pngBody},
		{name: "three redirects within the brand are followed",
			routes: map[string]http.HandlerFunc{
				touch:                  redirect("https://brand.test/1"),
				"https://brand.test/1": redirect("https://brand.test/2"),
				"https://brand.test/2": redirect("https://brand.test/3"),
				"https://brand.test/3": serve("image/png", pngBody)},
			wantExt: "png", wantCT: "image/png", body: pngBody},
		{name: "a fourth redirect is refused",
			routes: with(both(redirect("https://brand.test/1")), map[string]http.HandlerFunc{
				"https://brand.test/1": redirect("https://brand.test/2"),
				"https://brand.test/2": redirect("https://brand.test/3"),
				"https://brand.test/3": redirect("https://brand.test/4"),
				"https://brand.test/4": serve("image/png", pngBody)})},
		{name: "an HTML page that claims to be a PNG is refused",
			routes: both(serve("image/png", htmlBody))},
		{name: "an SVG is refused",
			routes: both(serve("image/svg+xml", svgBody))},
		{name: "an SVG that claims to be a PNG is refused",
			routes: both(serve("image/png", svgBody))},
		{name: "an empty body is refused",
			routes: both(serve("image/png", nil))},
		{name: "an error status is refused even with an image body",
			routes: both(func(rw http.ResponseWriter, r *http.Request) {
				rw.Header().Set("Content-Type", "image/png")
				rw.WriteHeader(http.StatusInternalServerError)
				rw.Write(pngBody)
			})},
		{name: "a body cut off mid-way is refused, though it starts like a PNG",
			routes: both(func(rw http.ResponseWriter, r *http.Request) {
				rw.Header().Set("Content-Length", "4096")
				rw.Write(pngBody)
			})},
		{name: "a body one byte over the limit is refused",
			routes: both(serveChunked("image/png", over))},
		{name: "a body that announces it is over the limit is refused",
			routes: both(serveSized("image/png", over))},
		{name: "a 10 MB body is refused",
			routes: both(serveChunked("image/png", huge))},
		{name: "a body exactly at the limit is kept",
			routes:  both(serveSized("image/png", exact)),
			wantExt: "png", wantCT: "image/png", body: exact},
		{name: "a 404 for the touch icon falls back to favicon.ico",
			routes:  map[string]http.HandlerFunc{fav: serve("image/vnd.microsoft.icon", icoBody)},
			wantExt: "ico", wantCT: "image/x-icon", body: icoBody},
		{name: "the type comes from the bytes, not from what the server says",
			routes:  map[string]http.HandlerFunc{touch: serve("text/html", jpgBody)},
			wantExt: "jpg", wantCT: "image/jpeg", body: jpgBody},
		{name: "a PNG wider than 512 px is refused",
			routes: both(serve("image/png", encode("png", 513, 1)))},
		{name: "a PNG taller than 512 px is refused",
			routes: both(serve("image/png", encode("png", 1, 513)))},
		{name: "a GIF wider than 512 px is refused",
			routes: both(serve("image/gif", encode("gif", 513, 1)))},
		{name: "a JPEG taller than 512 px is refused",
			routes: both(serve("image/jpeg", encode("jpeg", 1, 513)))},
		{name: "a PNG whose header is not a PNG header is refused",
			routes: both(serve("image/png", padded([]byte("\x89PNG\r\n\x1a\n"), 64)))},
		{name: "a PNG of exactly 512 by 512 px is kept",
			routes:  map[string]http.HandlerFunc{touch: serve("image/png", encode("png", 512, 512))},
			wantExt: "png", wantCT: "image/png", body: encode("png", 512, 512)},
		{name: "a GIF87a is kept",
			routes:  map[string]http.HandlerFunc{touch: serve("image/gif", gif87Body)},
			wantExt: "gif", wantCT: "image/gif", body: gif87Body},
		{name: "a GIF89a is kept",
			routes:  map[string]http.HandlerFunc{touch: serve("image/gif", gif89Body)},
			wantExt: "gif", wantCT: "image/gif", body: gif89Body},
		{name: "a WebP is kept",
			routes:  map[string]http.HandlerFunc{touch: serve("image/webp", webpBody)},
			wantExt: "webp", wantCT: "image/webp", body: webpBody},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, c.routes)
			err := w.f.Ensure(context.Background(), "brand", "brand.test")
			_, ct, _, ok := w.f.Open("brand")
			fails := failures(t, w.f.Dir)
			if c.wantExt == "" {
				if err == nil {
					t.Error("Ensure succeeded, want a refusal")
				}
				if ok {
					t.Errorf("Open serves a refused icon as %q", ct)
				}
				if got := names(t, w.f.Dir); len(got) != 1 || got[0] != "failures.json" {
					t.Errorf("dir holds %v, want only failures.json", got)
				}
				if at, rec := fails["brand"]; !rec || at != w.now.Unix() {
					t.Errorf("failure not recorded: %v", fails)
				}
				return
			}
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if !ok {
				t.Fatal("Open found nothing")
			}
			if ct != c.wantCT {
				t.Errorf("content type %q, want %q", ct, c.wantCT)
			}
			if got := names(t, w.f.Dir); len(got) != 1 || got[0] != "brand."+c.wantExt {
				t.Errorf("dir holds %v, want only brand.%s", got, c.wantExt)
			}
			r, _, _, _ := w.f.Open("brand")
			got, _ := io.ReadAll(r)
			if !bytes.Equal(got, c.body) {
				t.Errorf("stored %d bytes, want the %d fetched", len(got), len(c.body))
			}
			if len(fails) != 0 {
				t.Errorf("a success recorded a failure: %v", fails)
			}
		})
	}
}

func TestAFailureIsNotRetriedForAWeek(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{})
	ctx := context.Background()
	if err := w.f.Ensure(ctx, "brand", "brand.test"); err == nil {
		t.Fatal("Ensure against a site with no icon succeeded")
	}
	if n := w.hitCount(); n != 2 {
		t.Fatalf("a failing Ensure made %d requests, want 2 (both URLs)", n)
	}
	start := w.now

	// Six days and 23 hours later, after a restart, the failure still holds.
	w.now = start.Add(7*24*time.Hour - time.Hour)
	again := w.clone()
	if err := again.Ensure(ctx, "brand", "brand.test"); err != nil {
		t.Fatalf("a skipped Ensure returned %v, want nil", err)
	}
	if n := w.hitCount(); n != 2 {
		t.Fatalf("a recent failure was retried: %d requests in all", n)
	}

	// A week on, it is tried again, and this time the site has an icon.
	w.mu.Lock()
	w.routes["https://brand.test/apple-touch-icon.png"] = serve("image/png", pngBody)
	w.mu.Unlock()
	w.now = start.Add(7 * 24 * time.Hour)
	if err := again.Ensure(ctx, "brand", "brand.test"); err != nil {
		t.Fatalf("Ensure a week later: %v", err)
	}
	if n := w.hitCount(); n != 3 {
		t.Fatalf("after a week: %d requests in all, want 3", n)
	}
	if _, _, _, ok := again.Open("brand"); !ok {
		t.Error("the retried icon was not kept")
	}
	if m := failures(t, w.f.Dir); len(m) != 0 {
		t.Errorf("the old failure outlived the success: %v", m)
	}
}

func TestOnlyTheFailingAppWaits(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://good.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	ctx := context.Background()
	if err := w.f.Ensure(ctx, "bad", "bad.test"); err == nil {
		t.Fatal("Ensure for a site with no icon succeeded")
	}
	if err := w.f.Ensure(ctx, "good", "good.test"); err != nil {
		t.Fatalf("a failure for one app blocked another: %v", err)
	}
	m := failures(t, w.f.Dir)
	if _, ok := m["bad"]; !ok || len(m) != 1 {
		t.Errorf("failures %v, want only bad", m)
	}
}

func TestACorruptFailureListIsTreatedAsEmpty(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{})
	if err := os.MkdirAll(w.f.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.f.Dir, "failures.json"), []byte(`{"brand": 12`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.f.Ensure(context.Background(), "brand", "brand.test"); err == nil {
		t.Fatal("Ensure against a site with no icon succeeded")
	}
	if n := w.hitCount(); n != 2 {
		t.Errorf("a corrupt list stopped the fetch: %d requests", n)
	}
	if len(w.logs) != 1 || !strings.Contains(w.logs[0], "failures.json") {
		t.Errorf("logs %q, want one line naming failures.json", w.logs)
	}
	if m := failures(t, w.f.Dir); m["brand"] != w.now.Unix() {
		t.Errorf("the list was not rewritten whole: %v", m)
	}
	fi, err := os.Stat(filepath.Join(w.f.Dir, "failures.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("failures.json mode %o, want 600", perm)
	}
}

func TestAShutdownIsNotMistakenForABrokenSite(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.f.Ensure(ctx, "brand", "brand.test"); err == nil {
		t.Fatal("Ensure with a cancelled context succeeded")
	}
	if m := failures(t, w.f.Dir); len(m) != 0 {
		t.Errorf("a cancelled fetch was recorded as the site's failure: %v", m)
	}
}

// A site that never answers in time is not recorded as the site's failure:
// a router booting before its line is up, or a line that drops, times out
// on every brand, and recording that would leave each without an icon for
// a week. The error says it was the network.
func TestAnUnansweredSiteIsNotTheSitesFailure(t *testing.T) {
	// No answer to the connection itself: nothing came back over the line.
	w := newWorld(t, nil)
	tr := w.f.Client.Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
	}
	w.f.Client = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := w.f.Ensure(ctx, "brand", "brand.test")
	if err == nil {
		t.Fatal("Ensure against a silent line succeeded")
	}
	if !errors.Is(err, ErrNetwork) {
		t.Errorf("a connection nobody answered is the network's: %v", err)
	}
	if m := failures(t, w.f.Dir); len(m) != 0 {
		t.Errorf("a site that never answered was recorded: %v", m)
	}
}

// A site that took the connection and then said nothing until the deadline
// (a geo block, a bot wall) answered as far as the line goes: the failure is
// the site's, recorded and asked again in a week, and it cannot end a pass.
func TestASiteThatConnectsAndThenSaysNothingIsItsOwnFailure(t *testing.T) {
	release := make(chan struct{})
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": func(rw http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		},
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := w.f.Ensure(ctx, "brand", "brand.test")
	if err == nil {
		t.Fatal("Ensure against a silent site succeeded")
	}
	if errors.Is(err, ErrNetwork) {
		t.Errorf("a site that connected and went silent is not the network's: %v", err)
	}
	if m := failures(t, w.f.Dir); m["brand"] != w.now.Unix() {
		t.Errorf("a site that connected and went silent was not recorded: %v", m)
	}
}

// failingDial makes every connection fail the way err says.
func failingDial(w *world, err error) {
	tr := w.f.Client.Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, err }
	w.f.Client = &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

// Errors that say the network is not there (no name resolution, no route, a
// refused connection) are not the site's failure and are not recorded; an
// address that is not public, and anything the site itself answered, is.
func TestANetworkErrorIsNotTheSitesFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		dial    error
		network bool
	}{
		// A name the DNS says does not exist, and a host that refused the
		// connection, are answers about the site: the line was up to give them.
		"a name that does not exist": {&net.DNSError{Err: "no such host", Name: "brand.test", IsNotFound: true}, false},
		"no resolver answer":         {&net.DNSError{Err: "i/o timeout", Name: "brand.test", IsTimeout: true, IsTemporary: true}, true},
		"a temporary DNS failure":    {&net.DNSError{Err: "server misbehaving", Name: "brand.test", IsTemporary: true}, true},
		"connection refused":         {&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, false},
		"no network":                 {&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}, true},
		"no route":                   {&net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}, true},
		"not a public address":       {&net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("%w: 10.0.0.1", errNotPublic)}, false},
	} {
		w := newWorld(t, nil)
		failingDial(w, tc.dial)
		err := w.f.Ensure(context.Background(), "brand", "brand.test")
		if err == nil {
			t.Fatalf("%s: Ensure succeeded", name)
		}
		if got := errors.Is(err, ErrNetwork); got != tc.network {
			t.Errorf("%s: network error %v, want %v (%v)", name, got, tc.network, err)
		}
		m := failures(t, w.f.Dir)
		if recorded := m["brand"] == w.now.Unix(); recorded == tc.network {
			t.Errorf("%s: recorded %v, want %v: %v", name, recorded, !tc.network, m)
		}
	}

	// A site that answered one URL and then dropped the line still answered:
	// that is the site's failure.
	w := newWorld(t, nil) // every path is 404
	var n int
	tr := w.f.Client.Transport.(*http.Transport).Clone()
	inner := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		n++
		if n > 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		return inner(ctx, network, addr)
	}
	tr.DisableKeepAlives = true
	w.f.Client = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	if err := w.f.Ensure(context.Background(), "brand", "brand.test"); err == nil || errors.Is(err, ErrNetwork) {
		t.Errorf("a 404 then a dropped line is the site's failure: %v", err)
	}
	if m := failures(t, w.f.Dir); m["brand"] != w.now.Unix() {
		t.Errorf("and it is recorded: %v", m)
	}
}

// Due says whether Ensure would ask the site: not for an icon already kept,
// not while a failure holds, yes otherwise.
func TestDueSaysWhetherASiteWouldBeAsked(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://kept.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	if !w.f.Due("kept") {
		t.Error("an icon never fetched is due")
	}
	if err := w.f.Ensure(context.Background(), "kept", "kept.test"); err != nil {
		t.Fatal(err)
	}
	if w.f.Due("kept") {
		t.Error("an icon already kept is not due")
	}
	if err := w.f.Ensure(context.Background(), "broken", "broken.test"); err == nil {
		t.Fatal("a 404 site succeeded")
	}
	if w.f.Due("broken") {
		t.Error("a failure recorded just now holds")
	}
	w.now = w.now.Add(8 * 24 * time.Hour)
	if !w.f.Due("broken") {
		t.Error("a failure older than a week no longer holds")
	}
	if w.f.Due("Not An Id") || (&Fetcher{}).Due("x") {
		t.Error("an invalid id, or no directory, is never due")
	}
}

func TestHostileIdsAndDomainsAreRefused(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	ids := []string{
		"", "../brand", "a/b", `a\b`, ".brand", "-brand", "_brand", "Brand",
		"brand\x00", "brand.png/", "bränd", "a b", strings.Repeat("a", 65),
	}
	domains := []string{
		"", "https://brand.test", "brand.test:443", "brand.test/x", "brand.test?x",
		"user@brand.test", "brand..test", ".brand.test", "brand.test.", "-brand.test",
		"brand-.test", "brand", "localhost", "127.0.0.1", "10.0.0.1", "192.168.1", "brand.123", "[::1]", "::1",
		"бренд.test", "brand test", "brand.test\n", strings.Repeat("a", 64) + ".test",
		strings.Repeat("abcdefgh.", 29) + "test",
	}
	ctx := context.Background()
	for _, id := range ids {
		if err := w.f.Ensure(ctx, id, "brand.test"); err == nil {
			t.Errorf("Ensure accepted id %q", id)
		}
		if _, _, _, ok := w.f.Open(id); ok {
			t.Errorf("Open accepted id %q", id)
		}
	}
	for _, d := range domains {
		if err := w.f.Ensure(ctx, "brand", d); err == nil {
			t.Errorf("Ensure accepted domain %q", d)
		}
	}
	if n := w.hitCount(); n != 0 {
		t.Errorf("hostile input reached the network: %d requests", n)
	}
	if got := names(t, w.f.Dir); len(got) != 0 {
		t.Errorf("hostile input left files: %v", got)
	}

	// Sanity: the same fetcher takes a good pair, so the refusals above are
	// about the input and not a broken fetcher.
	if err := w.f.Ensure(ctx, "brand", "Brand.Test"); err != nil {
		t.Fatalf("a good id with a mixed-case domain was refused: %v", err)
	}
	// x.brand.test has no icon: what matters is that it was asked, twice.
	w.f.Ensure(ctx, "a.b_c-9", "x.brand.test")
	if n := w.hitCount(); n != 3 {
		t.Errorf("a good id and subdomain made %d requests in all, want 3", n)
	}
}

func TestOpenNeverReadsOutsideTheIconDir(t *testing.T) {
	w := newWorld(t, nil)
	root := filepath.Dir(w.f.Dir)
	if err := os.MkdirAll(w.f.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.png")
	if err := os.WriteFile(secret, pngBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := w.f.Open("../secret"); ok {
		t.Error("Open walked out of the icon dir")
	}
	if err := os.Symlink(secret, filepath.Join(w.f.Dir, "linked.png")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := w.f.Open("linked"); ok {
		t.Error("Open followed a symlink out of the icon dir")
	}
	if err := os.Mkdir(filepath.Join(w.f.Dir, "dir.png"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := w.f.Open("dir"); ok {
		t.Error("Open served a directory")
	}
	// A file planted under an icon name that is not the image its name says
	// is not served either.
	if err := os.WriteFile(filepath.Join(w.f.Dir, "fake.png"), svgBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := w.f.Open("fake"); ok {
		t.Error("Open served an SVG stored under a PNG name")
	}
	if err := os.WriteFile(filepath.Join(w.f.Dir, "vast.png"), encode("png", 600, 600), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := w.f.Open("vast"); ok {
		t.Error("Open served a planted PNG over 512 px")
	}
	if _, _, _, ok := w.f.Open("missing"); ok {
		t.Error("Open found an icon that was never fetched")
	}
}

func TestOpenNeverSeesAHalfWrittenIcon(t *testing.T) {
	body := padded(pngBody, 60000)
	routes := map[string]http.HandlerFunc{}
	for i := range 20 {
		routes[fmt.Sprintf("https://b%d.brand.test/apple-touch-icon.png", i)] = serveChunked("image/png", body)
	}
	w := newWorld(t, routes)

	done := make(chan struct{})
	var wg sync.WaitGroup
	var bad []string
	var mu sync.Mutex
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				for i := range 20 {
					r, _, _, ok := w.f.Open(fmt.Sprintf("b%d", i))
					if !ok {
						continue
					}
					got, _ := io.ReadAll(r)
					if !bytes.Equal(got, body) {
						mu.Lock()
						bad = append(bad, fmt.Sprintf("b%d: %d bytes", i, len(got)))
						mu.Unlock()
					}
				}
			}
		}()
	}
	for i := range 20 {
		if err := w.f.Ensure(context.Background(), fmt.Sprintf("b%d", i), fmt.Sprintf("b%d.brand.test", i)); err != nil {
			t.Errorf("Ensure b%d: %v", i, err)
		}
	}
	close(done)
	wg.Wait()
	if len(bad) > 0 {
		t.Errorf("Open saw partial icons: %v", bad)
	}
}

func TestAFetcherWithoutADirRefuses(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	noDir := w.clone()
	noDir.Dir = ""
	if err := noDir.Ensure(context.Background(), "brand", "brand.test"); err == nil {
		t.Error("a fetcher without a dir wrote somewhere")
	}
	if n := w.hitCount(); n != 0 {
		t.Errorf("%d requests, want none", n)
	}
}

func TestTheDocumentedDefaultsApply(t *testing.T) {
	limit := DefaultMaxBytes
	exact := padded(pngBody, limit)
	over := padded(pngBody, limit+1)
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://exact.test/apple-touch-icon.png": serveChunked("image/png", exact),
		"https://over.test/apple-touch-icon.png":  serveChunked("image/png", over),
	})
	w.f.MaxBytes = 0
	w.f.Retry = 0
	ctx := context.Background()
	if limit != 65536 {
		t.Errorf("DefaultMaxBytes is %d, want 64 KiB", limit)
	}
	if err := w.f.Ensure(ctx, "exact", "exact.test"); err != nil {
		t.Errorf("a 64 KiB icon was refused with no MaxBytes set: %v", err)
	}
	if err := w.f.Ensure(ctx, "over", "over.test"); err == nil {
		t.Error("a 64 KiB + 1 icon was kept with no MaxBytes set")
	}
	hits := w.hitCount()
	start := w.now
	w.now = start.Add(7*24*time.Hour - time.Second)
	w.f.Ensure(ctx, "over", "over.test")
	if w.hitCount() != hits {
		t.Error("with no Retry set, a failure was retried within a week")
	}
	w.now = start.Add(7 * 24 * time.Hour)
	w.f.Ensure(ctx, "over", "over.test")
	if w.hitCount() == hits {
		t.Error("with no Retry set, a failure was not retried after a week")
	}
}

func TestFailuresThatNoLongerHoldAreForgotten(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://future.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	if err := os.MkdirAll(w.f.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// "old" failed eight days ago, "future" carries a stamp from a clock that
	// ran ahead, and "../x" is not an id at all.
	list := fmt.Sprintf(`{"old": %d, "future": %d, "../x": %d}`,
		w.now.Add(-8*24*time.Hour).Unix(), w.now.Add(24*time.Hour).Unix(), w.now.Unix())
	if err := os.WriteFile(filepath.Join(w.f.Dir, "failures.json"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := w.f.Ensure(ctx, "future", "future.test"); err != nil {
		t.Fatalf("a failure stamped in the future held the fetch back: %v", err)
	}
	if err := w.f.Ensure(ctx, "new", "new.test"); err == nil {
		t.Fatal("Ensure for a site with no icon succeeded")
	}
	m := failures(t, w.f.Dir)
	if len(m) != 1 || m["new"] != w.now.Unix() {
		t.Errorf("failures %v, want only the new one", m)
	}
}

func TestLeftoversFromACrashAreCleared(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	if err := os.MkdirAll(w.f.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(w.f.Dir, ".brand.123456.tmp")
	if err := os.WriteFile(left, pngBody[:5], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.f.Ensure(context.Background(), "brand", "brand.test"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, w.f.Dir); len(got) != 1 || got[0] != "brand.png" {
		t.Errorf("dir holds %v, want only brand.png", got)
	}
}

func TestAStoredFileOverTheLimitIsNotServed(t *testing.T) {
	w := newWorld(t, nil)
	if err := os.MkdirAll(w.f.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	big := padded(pngBody, maxBytes+8)
	if err := os.WriteFile(filepath.Join(w.f.Dir, "big.png"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := w.f.Open("big"); ok {
		t.Error("Open served a file larger than MaxBytes")
	}
	if err := os.WriteFile(filepath.Join(w.f.Dir, "empty.png"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := w.f.Open("empty"); ok {
		t.Error("Open served an empty file")
	}
}

func TestADirThatIsAFileIsReported(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.f.Dir = file
	if err := w.f.Ensure(context.Background(), "brand", "brand.test"); err == nil {
		t.Error("Ensure with a file for a dir reported success")
	}
	if n := w.hitCount(); n != 0 {
		t.Errorf("%d requests with nowhere to keep the answer", n)
	}
}

func TestNoCookieRidesFromOneRequestToTheNext(t *testing.T) {
	var cookies []string
	var mu sync.Mutex
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": func(rw http.ResponseWriter, r *http.Request) {
			http.SetCookie(rw, &http.Cookie{Name: "track", Value: "router-1"})
			http.NotFound(rw, r)
		},
		"https://brand.test/favicon.ico": func(rw http.ResponseWriter, r *http.Request) {
			mu.Lock()
			cookies = append(cookies, r.Header.Get("Cookie"))
			mu.Unlock()
			rw.Write(icoBody)
		},
	})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	w.f.Client.Jar = jar
	if err := w.f.Ensure(context.Background(), "brand", "brand.test"); err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0] != "" {
		t.Errorf("favicon.ico was asked with cookies %q, want none", cookies)
	}
}

func TestTheFetcherNeverConnectsToPrivateAddresses(t *testing.T) {
	refused := []string{
		"127.0.0.1", "127.1.2.3", "::1", // loopback
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", // RFC 1918
		"100.64.0.1", "100.127.255.255", // carrier-grade NAT, also Tailscale
		"169.254.169.254", "fe80::1", // link-local
		"fc00::1", "fd12:3456::1", // ULA
		"224.0.0.1", "239.255.255.250", "ff02::1", // multicast
		"0.0.0.0", "::", "0.1.2.3", // unspecified and "this network"
		"255.255.255.255", "240.0.0.1", // broadcast and reserved
		"::ffff:192.168.1.1", "::ffff:127.0.0.1", "::ffff:100.64.0.1", // IPv4 inside IPv6
	}
	allowed := []string{
		"8.8.8.8", "1.1.1.1", "172.15.255.255", "172.32.0.1", "100.63.255.255",
		"100.128.0.1", "192.169.0.1", "2606:4700::1111", "2a00:1450:4009::200e",
		"::ffff:8.8.8.8",
	}
	for _, a := range refused {
		err := guardDial("tcp", net.JoinHostPort(a, "443"), nil)
		if err == nil || !errors.Is(err, errNotPublic) {
			t.Errorf("dialling %s was allowed (err %v)", a, err)
		}
	}
	for _, a := range allowed {
		if err := guardDial("tcp", net.JoinHostPort(a, "443"), nil); err != nil {
			t.Errorf("dialling public %s was refused: %v", a, err)
		}
	}
	for _, a := range []string{"", "nonsense", "brand.test:443", "10.0.0.1"} {
		if err := guardDial("tcp", a, nil); err == nil {
			t.Errorf("an address the dialer should never hand over, %q, was allowed", a)
		}
	}

	// The guard is wired into the client the router uses, and into the one a
	// Fetcher falls back to without its own: both refuse the loopback test
	// server that the injected test clients reach.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write(pngBody)
	}))
	defer srv.Close()
	for name, c := range map[string]*http.Client{
		"NewClient":        NewClient(2 * time.Second),
		"the nil fallback": (&Fetcher{}).client(),
	} {
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s connected to %s", name, srv.URL)
			continue
		}
		if !errors.Is(err, errNotPublic) {
			t.Errorf("%s failed for another reason: %v", name, err)
		}
	}
	if c := NewClient(3 * time.Second); c.Timeout != 3*time.Second || c.Jar != nil {
		t.Errorf("NewClient: timeout %v, jar %v", c.Timeout, c.Jar)
	}
}

func TestAddressesInsideIPv6AreJudgedByTheIPv4TheyCarry(t *testing.T) {
	refused := []string{
		"64:ff9b::a00:1",                       // NAT64 well-known prefix carrying 10.0.0.1
		"64:ff9b::7f00:1",                      // ... carrying 127.0.0.1
		"64:ff9b::a9fe:a9fe",                   // ... carrying 169.254.169.254
		"64:ff9b:1::c0a8:101",                  // local-use NAT64, /96 layout, 192.168.1.1
		"64:ff9b:1:c0a8:1:100::",               // local-use NAT64, /48 layout, 192.168.1.1
		"2002:c0a8:101::1",                     // 6to4 carrying 192.168.1.1
		"2002:7f00:1::",                        // 6to4 carrying 127.0.0.1
		"2002:6440:1::1",                       // 6to4 carrying 100.64.0.1
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", // Teredo
		"::808:808",                            // IPv4-compatible, deprecated
		"fec0::1",                              // site-local, deprecated
		"100::1",                               // discard
		"2001:db8::1",                          // documentation
	}
	allowed := []string{
		"64:ff9b::808:808",      // NAT64 carrying 8.8.8.8: DNS64 lines must still work
		"64:ff9b:1::808:808",    // local-use NAT64, /96 layout, 8.8.8.8
		"64:ff9b:1:808:8:800::", // local-use NAT64, /48 layout, 8.8.8.8
		"2002:808:808::1",       // 6to4 carrying 8.8.8.8
		"2001:4860:4860::8888",  // a public address next to Teredo's /32
		"2001:db9::1",           // next to documentation
	}
	for _, a := range refused {
		if publicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s was taken for a public address", a)
		}
	}
	for _, a := range allowed {
		if !publicAddr(netip.MustParseAddr(a)) {
			t.Errorf("public %s was refused", a)
		}
	}
}

func TestNewClientTakesNoProxyAndBoundsTheHeaders(t *testing.T) {
	tr, ok := NewClient(time.Second).Transport.(*http.Transport)
	if !ok {
		t.Fatal("NewClient's transport is not an *http.Transport")
	}
	// A proxy from the environment would carry every brand's name to it and
	// dial the proxy, not the brand, past the address guard.
	if tr.Proxy != nil {
		t.Error("NewClient takes a proxy")
	}
	if tr.MaxResponseHeaderBytes != 16<<10 {
		t.Errorf("response headers bounded at %d bytes, want 16 KiB", tr.MaxResponseHeaderBytes)
	}
}

func TestASymlinkedIconDirIsRefused(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	real := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "planted.png"), pngBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, w.f.Dir); err != nil {
		t.Fatal(err)
	}
	if err := w.f.Ensure(context.Background(), "brand", "brand.test"); err == nil {
		t.Error("Ensure wrote through a symlinked icon dir")
	}
	if got := names(t, real); len(got) != 1 {
		t.Errorf("the link's target now holds %v", got)
	}
	if _, _, _, ok := w.f.Open("planted"); ok {
		t.Error("Open served a file through a symlinked icon dir")
	}
}

func TestALooseIconDirIsTightened(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://brand.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	if err := os.Mkdir(w.f.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(w.f.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.f.Ensure(context.Background(), "brand", "brand.test"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(w.f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("icon dir left at %o, want 700", perm)
	}
}

func TestConcurrentEnsuresLoseNoFailure(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.f.Ensure(context.Background(), fmt.Sprintf("app%d", i), fmt.Sprintf("app%d.test", i))
		}()
	}
	wg.Wait()
	if m := failures(t, w.f.Dir); len(m) != 16 {
		t.Errorf("%d of 16 failures survived concurrent writers: %v", len(m), m)
	}
}

func TestAFailureThatCannotBeSavedStillHoldsInThisProcess(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{})
	// A directory where failures.json belongs: every save fails, as on a
	// full overlay.
	if err := os.MkdirAll(filepath.Join(w.f.Dir, "failures.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := w.f.Ensure(ctx, "brand", "brand.test"); err == nil {
		t.Fatal("Ensure against a site with no icon succeeded")
	}
	hits := w.hitCount()
	w.now = w.now.Add(24 * time.Hour)
	w.f.Ensure(ctx, "brand", "brand.test")
	if w.hitCount() != hits {
		t.Error("a failure that could not be saved was retried the next day")
	}
	w.now = w.now.Add(7 * 24 * time.Hour)
	w.f.Ensure(ctx, "brand", "brand.test")
	if w.hitCount() == hits {
		t.Error("a failure held in memory was never retried")
	}
}

func TestCrashLeftoversOfAnotherIdAreLeftAlone(t *testing.T) {
	w := newWorld(t, map[string]http.HandlerFunc{
		"https://a.test/apple-touch-icon.png": serve("image/png", pngBody),
	})
	if err := os.MkdirAll(w.f.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// ".a.b.123.tmp" belongs to id "a.b", ".a.456.tmp" to id "a".
	for _, n := range []string{".a.b.123.tmp", ".a.456.tmp", ".a.tmpx.789.tmp"} {
		if err := os.WriteFile(filepath.Join(w.f.Dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.f.Ensure(context.Background(), "a", "a.test"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(t, w.f.Dir), " ")
	if got != ".a.b.123.tmp .a.tmpx.789.tmp a.png" {
		t.Errorf("dir holds %q, want the other ids' leftovers kept and a's gone", got)
	}
}
