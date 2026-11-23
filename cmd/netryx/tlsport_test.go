package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		t.Fatalf("%s is not PEM", path)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Without uhttpd's certificate the console makes its own, once: ECDSA P-256,
// self-signed, naming the address it listens on, the key readable by root
// alone.
func TestWithoutUhttpdsPairTheConsoleMakesItsOwn(t *testing.T) {
	own := filepath.Join(t.TempDir(), "netryx")
	gone := filepath.Join(t.TempDir(), "none")
	conf := tlsFor("192.168.1.1:8788", gone+".crt", gone+".key", own)
	if conf == nil {
		t.Fatal("no TLS on a house-side address without uhttpd's pair")
	}
	crt, key := filepath.Join(own, "console.crt"), filepath.Join(own, "console.key")
	fi, err := os.Stat(key)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key %v (%v), want mode 0600", fi, err)
	}
	c := readCert(t, crt)
	pub, ok := c.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("key %T, want ECDSA P-256", c.PublicKey)
	}
	if !c.IPAddresses[0].Equal(net.ParseIP("192.168.1.1")) {
		t.Errorf("certificate names %v, want the listen address", c.IPAddresses)
	}
	if c.NotAfter.Before(time.Now().AddDate(5, 0, 0)) {
		t.Errorf("certificate ends %v: it is made once and kept, so it must last", c.NotAfter)
	}
	first, _ := os.ReadFile(crt)
	if tlsFor("192.168.1.1:8788", gone+".crt", gone+".key", own) == nil {
		t.Fatal("second start: no TLS")
	}
	if again, _ := os.ReadFile(crt); !bytes.Equal(first, again) {
		t.Error("a second start made a new certificate: the one the browser accepted must stay")
	}
}

func TestUhttpdsPairIsPreferred(t *testing.T) {
	theirs := filepath.Join(t.TempDir(), "uhttpd")
	if tlsFor("192.168.1.1:8788", "/nonexistent.crt", "/nonexistent.key", theirs) == nil {
		t.Fatal("setup")
	}
	own := filepath.Join(t.TempDir(), "netryx")
	if tlsFor("192.168.1.1:8788", filepath.Join(theirs, "console.crt"), filepath.Join(theirs, "console.key"), own) == nil {
		t.Fatal("no TLS with uhttpd's pair")
	}
	if _, err := os.Stat(own); err == nil {
		t.Error("a certificate of its own was made although uhttpd's pair exists")
	}
}

func TestTheLoopbackTunnelStaysPlain(t *testing.T) {
	own := filepath.Join(t.TempDir(), "netryx")
	if tlsFor("127.0.0.1:8788", "/nonexistent.crt", "/nonexistent.key", own) != nil {
		t.Error("TLS on loopback: the SSH tunnel stays plain http")
	}
	if _, err := os.Stat(own); err == nil {
		t.Error("a certificate was made for a loopback console")
	}
}

// One port speaks both: a TLS client gets the console, a plain http request
// from the house is sent to https on the same port (for one of the router's
// own names only), and from the router itself (the SSH tunnel) it is served.
func TestPlainHTTPOnTheHTTPSPortIsSentToHTTPS(t *testing.T) {
	o := testOptions(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	o.Listen = ln.Addr().String()
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	next := c.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "console") }))
	conf := tlsFor("192.168.1.1:8788", "/nonexistent.crt", "/nonexistent.key", t.TempDir())
	tunnel := false
	redirect := true
	tlsLn, plainLn := splitTLS(ln)
	go (&http.Server{Handler: next, TLSConfig: conf}).ServeTLS(tlsLn, "", "")
	go (&http.Server{Handler: c.plainSide(next, func() bool { return redirect }, func(string) bool { return tunnel })}).Serve(plainLn)
	t.Cleanup(func() { ln.Close() })
	base := "://" + ln.Addr().String()

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	res, err := noFollow.Get("http" + base + "/flows?q=x")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != "https"+base+"/flows?q=x" {
		t.Fatalf("plain http from the house: %d %q, want 308 to https on the same port", res.StatusCode, res.Header.Get("Location"))
	}
	// Never to a name that is not the router's.
	q, _ := http.NewRequest(http.MethodGet, "http"+base+"/", nil)
	q.Host = "evil.example"
	res, err = noFollow.Do(q)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMisdirectedRequest || res.Header.Get("Location") != "" {
		t.Fatalf("a foreign Host: %d %q, want 421 and no redirect", res.StatusCode, res.Header.Get("Location"))
	}
	// TLS on the same port is the console.
	tc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second}
	res, err = tc.Get("https" + base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "console" {
		t.Fatalf("https: %d %q", res.StatusCode, body)
	}
	// The tunnel: plain http from the router itself is served as it is.
	tunnel = true
	res, err = noFollow.Get("http" + base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "console" {
		t.Fatalf("plain http through the tunnel: %d %q", res.StatusCode, body)
	}
	// With the redirect turned off, plain http from the house is refused.
	tunnel, redirect = false, false
	res, err = noFollow.Get("http" + base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || res.Header.Get("Location") != "" || !strings.Contains(string(body), "https") {
		t.Fatalf("redirect off: %d %q %q, want 400 saying https", res.StatusCode, res.Header.Get("Location"), body)
	}
}

// A client that connects and says nothing is let go, and holds nothing else up.
func TestASilentConnectionIsLetGo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	old := peekTimeout
	peekTimeout = 200 * time.Millisecond
	t.Cleanup(func() { peekTimeout = old })
	_, plainLn := splitTLS(ln)
	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })}).Serve(plainLn)
	silent, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	res, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("a request behind a silent connection: %v", err)
	}
	res.Body.Close()
	silent.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := silent.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the silent connection: %v, want it closed (EOF)", err)
	}
}

// On 0.0.0.0 or :: the console also listens on the WAN address. A request
// that arrived there is refused whatever Host it names, since the Host header
// is the client's to write; only the router's house-side addresses and
// loopback are served.
func TestARequestArrivingOnTheWANAddressIsRefused(t *testing.T) {
	o := testOptions(t)
	o.Listen = "0.0.0.0:8788"
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	h := c.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "console") }))
	for arrived, want := range map[string]int{
		"203.0.113.7":    http.StatusMisdirectedRequest, // the router's WAN address
		"192.168.50.1":   http.StatusOK,                 // the house bridge
		"192.168.2.1":    http.StatusOK,                 // the guest bridge
		"2001:db8:50::1": http.StatusOK,
		"127.0.0.1":      http.StatusOK,                 // the SSH tunnel
		"192.168.50.10":  http.StatusMisdirectedRequest, // not the router's own
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
		r.Host = "192.168.50.1:8788" // a Host the router would answer
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(arrived), Port: 8788}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("arrived on %s: %d, want %d", arrived, rec.Code, want)
		}
	}
}

// errListener fails Accept with a temporary error a few times (EMFILE when
// the router runs out of file descriptors), then hands out real connections.
type errListener struct {
	net.Listener
	fails int
}

type tempErr struct{}

func (tempErr) Error() string   { return "too many open files" }
func (tempErr) Timeout() bool   { return false }
func (tempErr) Temporary() bool { return true }

func (l *errListener) Accept() (net.Conn, error) {
	if l.fails > 0 {
		l.fails--
		return nil, tempErr{}
	}
	return l.Listener.Accept()
}

// Running out of file descriptors is a pause, not the end: the port keeps
// answering once descriptors are free again, so nobody in the house can stop
// the console by opening connections.
func TestRunningOutOfDescriptorsDoesNotCloseThePort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	_, plainLn := splitTLS(&errListener{Listener: ln, fails: 3})
	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })}).Serve(plainLn)
	res, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("after three EMFILE: %v", err)
	}
	res.Body.Close()
}
