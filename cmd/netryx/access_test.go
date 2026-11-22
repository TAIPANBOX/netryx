package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"netryx/internal/auth"
	"netryx/internal/export"
	"netryx/internal/web"
)

// fakeRPC stands in for rpcd: root/pw may view and control. err, when set,
// is what every call returns (rpcd down); it may change while a prober runs.
type fakeRPC struct {
	mu     sync.Mutex
	err    error
	logins int
}

const fakeSID = "0123456789abcdef0123456789abcdef"

func (f *fakeRPC) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeRPC) Login(_ context.Context, user, pass string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins++
	if f.err != nil {
		return "", f.err
	}
	if user != "root" || pass != "pw" {
		return "", auth.ErrBadLogin
	}
	return fakeSID, nil
}

func (f *fakeRPC) Access(_ context.Context, sid, scope, object, function string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	if sid != fakeSID {
		return false, auth.ErrSessionGone
	}
	return scope == auth.Scope, nil
}

func (f *fakeRPC) Destroy(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// The served handler, as serve builds it: the Host check outermost, then
// the login, then the console and the scrape endpoints. Every view of the
// console, every API route among them, needs a session; only the page, its
// files and the icons answer without one.
func TestEveryViewNeedsALogin(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	gate := c.newGate(&fakeRPC{}, "tok")
	h := c.serveHandler(serveMux(c, func() export.Snapshot { return export.Snapshot{} }), gate)
	handle := ""
	do := func(method, path, remote, host string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = remote
		r.Host = host
		if cookie {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: handle})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	const house, router = "192.168.50.10:40000", "192.168.50.1:8788"
	login := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"root","password":"pw"}`))
	login.RemoteAddr, login.Host = house, router
	login.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, login)
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			handle = c.Value
		}
	}
	if rec.Code != http.StatusOK || handle == "" || handle == fakeSID {
		t.Fatalf("login through the served handler: %d %s, cookie %q", rec.Code, rec.Body, handle)
	}
	// The rpcd session id itself is no cookie.
	r := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	r.RemoteAddr, r.Host = house, router
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: fakeSID})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the rpcd session id as the cookie: %d", rec.Code)
	}
	mac := "02:4e:58:00:00:01"
	for _, p := range []string{
		"/api/overview", "/api/devices", "/api/devices/" + mac, "/api/devices/" + mac + "/traffic",
		"/api/devices/" + mac + "/apps", "/api/devices/" + mac + "/destinations",
		"/api/devices/" + mac + "/live", "/api/flows", "/api/session", "/api/nothing", "/index.html",
	} {
		rec := do(http.MethodGet, p, house, router, false)
		var e struct {
			Error string `json:"error"`
		}
		if rec.Code != http.StatusUnauthorized || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error != "login" {
			t.Errorf("%s without a session: %d %s", p, rec.Code, rec.Body)
		}
	}
	if rec := do(http.MethodPost, "/api/logout", house, router, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("a logout without a session: %d", rec.Code)
	}
	// The page, its script and the icon manifest answer 200 without a
	// session. A test tree may lack the built page (web/build.sh has not
	// run) or the icon pack (no data/): then the page's own handler answers
	// 503 or 404, and its CSP shows the request got past the gate to it.
	for _, p := range []string{"/", "/app.js", "/icons/manifest.json"} {
		rec := do(http.MethodGet, p, house, router, false)
		csp := rec.Header().Get("Content-Security-Policy")
		switch {
		case rec.Code == http.StatusOK && csp != "":
		case !web.Built() && csp != "" && (rec.Code == http.StatusServiceUnavailable || rec.Code == http.StatusNotFound):
			t.Logf("%s: %d from the page's handler, the page is not built in this tree", p, rec.Code)
		case web.Built() && p == "/icons/manifest.json" && rec.Code == http.StatusNotFound && csp != "":
			t.Logf("%s: 404 from the page's handler, the page was built without the icon pack", p)
		default:
			t.Errorf("%s answers without a session with 200: %d %q", p, rec.Code, csp)
		}
	}
	// With a session the same routes are the console's.
	for _, p := range []string{"/api/overview", "/api/devices", "/api/flows"} {
		if rec := do(http.MethodGet, p, house, router, true); rec.Code != http.StatusOK {
			t.Errorf("%s with a session: %d %s", p, rec.Code, rec.Body)
		}
	}
	// A MAC nobody has seen is the API's own 404, past the gate.
	if rec := do(http.MethodGet, "/api/devices/"+mac+"/live", house, router, true); rec.Code != http.StatusNotFound {
		t.Errorf("the live view of an unknown MAC with a session: %d %s", rec.Code, rec.Body)
	}
	// The scrape endpoints: loopback yes, the house side no.
	for _, p := range []string{"/metrics", "/api/snapshot", "/api/zabbix/discovery"} {
		if rec := do(http.MethodGet, p, house, router, true); rec.Code != http.StatusForbidden {
			t.Errorf("%s from the house without the token: %d", p, rec.Code)
		}
		if rec := do(http.MethodGet, p, "127.0.0.1:40000", "127.0.0.1:8788", false); rec.Code != http.StatusOK {
			t.Errorf("%s from loopback: %d", p, rec.Code)
		}
	}
	// The Host check still comes first, for the login too.
	for _, p := range []string{"/api/login", "/", "/metrics"} {
		if rec := do(http.MethodPost, p, house, "evil.example", false); rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s under a foreign name: %d", p, rec.Code)
		}
	}
}

// Without rpcd no login is possible, so a console set to listen on the
// house side listens on loopback instead; loopback stays loopback, and a
// router whose rpcd answers keeps what was configured.
func TestNoRPCKeepsTheConsoleOnLoopback(t *testing.T) {
	down := fmt.Errorf("%w: connection refused", auth.ErrNoRPC)
	for _, c := range []struct {
		addr string
		err  error
		want string
	}{
		{"192.168.1.1:8788", down, "127.0.0.1:8788"},
		{"0.0.0.0:9000", down, "127.0.0.1:9000"},
		{":8788", down, "127.0.0.1:8788"},
		{"[::]:8788", down, "127.0.0.1:8788"},
		{"router.lan:8788", down, "127.0.0.1:8788"},
		{"127.0.0.1:8788", down, "127.0.0.1:8788"},
		{"[::1]:8788", down, "[::1]:8788"},
		{"192.168.1.1:8788", nil, "192.168.1.1:8788"},
		{"192.168.1.1:8788", auth.ErrBadLogin, "192.168.1.1:8788"},
	} {
		if got := listenAddr(c.addr, c.err); got != c.want {
			t.Errorf("listenAddr(%q, %v) = %q, want %q", c.addr, c.err, got, c.want)
		}
	}

	// The probe is asked a few times before rpcd is called absent (it
	// may start after netryx at boot), never for a loopback listener.
	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }
	f := &fakeRPC{err: down}
	if got := decideListen("192.168.1.1:8788", f, sleep); got != "127.0.0.1:8788" {
		t.Errorf("rpcd down, house address: listens on %q", got)
	}
	if f.logins < 3 || len(slept) < 2 {
		t.Errorf("rpcd was asked %d times, %d waits: one failed probe is not enough", f.logins, len(slept))
	}
	f = &fakeRPC{err: down}
	if got := decideListen("127.0.0.1:8788", f, sleep); got != "127.0.0.1:8788" || f.logins != 0 {
		t.Errorf("loopback: %q after %d probes", got, f.logins)
	}
	f = &fakeRPC{}
	if got := decideListen("192.168.1.1:8788", f, sleep); got != "192.168.1.1:8788" || f.logins != 1 {
		t.Errorf("rpcd up: %q after %d probes", got, f.logins)
	}

	// armListen, as serve calls it: the gate learns there is no rpcd (reads
	// from loopback pass, changes are refused), and the Host check is told
	// the address actually listened on.
	o := testOptions(t)
	o.Listen = "router.lan:8788" // as serve builds it, from -addr
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	snap := func() export.Snapshot { return export.Snapshot{} }
	req := func(h http.Handler, method, path, host string) int {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:40000", host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	g := &auth.Gate{RPC: &fakeRPC{err: down}, Now: time.Now, Secret: auth.NewSecret()}
	if got := c.armListen("router.lan:8788", g, sleep, nil); got != "127.0.0.1:8788" || !g.NoRPC() {
		t.Fatalf("rpcd down: listens on %q, gate without rpcd %v", got, g.NoRPC())
	}
	h := c.serveHandler(serveMux(c, snap), g)
	if code := req(h, http.MethodGet, "/api/overview", "127.0.0.1:8788"); code != http.StatusOK {
		t.Errorf("without rpcd a loopback read passes: %d", code)
	}
	if code := req(h, http.MethodPost, "/api/overview", "127.0.0.1:8788"); code != http.StatusServiceUnavailable {
		t.Errorf("without rpcd a change is refused with 503: %d", code)
	}
	if code := req(h, http.MethodGet, "/api/overview", "router.lan:8788"); code != http.StatusMisdirectedRequest {
		t.Errorf("the name no longer listened on is refused by the Host check: %d", code)
	}
	// A loopback listener is not held back by the probe: the gate learns
	// about rpcd in the background.
	g = &auth.Gate{RPC: &fakeRPC{err: down}, Now: time.Now, Secret: auth.NewSecret()}
	if got := c.armListen("127.0.0.1:8788", g, sleep, nil); got != "127.0.0.1:8788" {
		t.Errorf("loopback: %q", got)
	}
	for i := 0; i < 200 && !g.NoRPC(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !g.NoRPC() {
		t.Error("loopback without rpcd: the gate never learned it")
	}
	g = &auth.Gate{RPC: &fakeRPC{}, Now: time.Now, Secret: auth.NewSecret()}
	c.armListen("127.0.0.1:8788", g, sleep, nil)
	time.Sleep(50 * time.Millisecond)
	h = c.serveHandler(serveMux(c, snap), g)
	if g.NoRPC() || req(h, http.MethodGet, "/api/overview", "127.0.0.1:8788") != http.StatusUnauthorized {
		t.Error("with rpcd answering a loopback read still needs a session")
	}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func selfSigned(t *testing.T, key any, pub any) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "OpenWrt"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// uhttpd's own pair is DER (px5g selfsigned -der, or openssl -outform der
// with a PEM key); a PEM pair works as well. A pair that does not match is
// refused, and the console is served over TLS only on a house address with
// both files present.
func TestUhttpdsOwnCertificateIsServed(t *testing.T) {
	dir := t.TempDir()
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecCert := selfSigned(t, ec, &ec.PublicKey)
	sec1, _ := x509.MarshalECPrivateKey(ec)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(ec)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaCert := selfSigned(t, rk, &rk.PublicKey)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherKey, _ := x509.MarshalECPrivateKey(other)

	file := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pemFile := func(name, typ string, der []byte) string {
		p := filepath.Join(dir, name)
		writePEM(t, p, typ, der)
		return p
	}
	good := []struct{ name, cert, key string }{
		{"px5g ec, DER pair", file("ec.crt", ecCert), file("ec.key", sec1)},
		{"DER cert, PKCS8 DER key", file("ec2.crt", ecCert), file("ec8.key", pkcs8)},
		{"openssl, DER cert and PEM key", file("ec3.crt", ecCert), pemFile("ec3.key", "PRIVATE KEY", pkcs8)},
		{"PEM pair", pemFile("ec.pem", "CERTIFICATE", ecCert), pemFile("eck.pem", "EC PRIVATE KEY", sec1)},
		{"px5g rsa, DER pair", file("rsa.crt", rsaCert), file("rsa.key", x509.MarshalPKCS1PrivateKey(rk))},
	}
	for _, g := range good {
		if _, err := loadKeyPair(g.cert, g.key); err != nil {
			t.Errorf("%s: %v", g.name, err)
		}
	}
	for _, b := range []struct{ name, cert, key string }{
		{"a key of another certificate", file("m.crt", ecCert), file("m.key", otherKey)},
		{"no key file", file("n.crt", ecCert), filepath.Join(dir, "missing.key")},
		{"garbage", file("g.crt", []byte("not a certificate")), file("g.key", []byte("not a key"))},
	} {
		if _, err := loadKeyPair(b.cert, b.key); err == nil {
			t.Errorf("%s was accepted", b.name)
		}
	}
	crt, key := good[0].cert, good[0].key
	if c := tlsFor("192.168.1.1:8788", crt, key); c == nil || len(c.Certificates) != 1 || c.MinVersion < 0x0303 {
		t.Errorf("a house address with uhttpd's pair is served over TLS 1.2+: %+v", c)
	}
	if c := tlsFor("127.0.0.1:8788", crt, key); c != nil {
		t.Error("loopback stays plain http")
	}
	if c := tlsFor("192.168.1.1:8788", filepath.Join(dir, "none.crt"), key); c != nil {
		t.Error("no certificate, plain http (the page warns)")
	}
	if c := tlsFor("192.168.1.1:8788", good[0].cert, filepath.Join(dir, "m.key")); c != nil {
		t.Error("a pair that does not match is not served")
	}
}

// rpcd that comes up after netryx (boot order, uhttpd restarting in an
// upgrade) closes the loopback door again: the prober finds it within its
// period, loopback reads need a session from then on, and a login works.
// The prober ends with serve.
func TestRPCComingBackClosesTheLoopbackDoor(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	tick := make(chan time.Time)
	var mu sync.Mutex
	var periods []time.Duration
	c.after = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		periods = append(periods, d)
		mu.Unlock()
		return tick
	}
	down := fmt.Errorf("%w: connection refused", auth.ErrNoRPC)
	f := &fakeRPC{err: down}
	g := c.newGate(f, "")
	stop := make(chan struct{})
	defer close(stop)
	c.armListen("127.0.0.1:8788", g, func(time.Duration) {}, stop)
	h := c.serveHandler(serveMux(c, func() export.Snapshot { return export.Snapshot{} }), g)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr, r.Host = "127.0.0.1:40000", "127.0.0.1:8788"
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	waitFor := func(want bool) {
		for i := 0; i < 400 && g.NoRPC() != want; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		if g.NoRPC() != want {
			t.Fatalf("the gate's no-rpcd state did not become %v", want)
		}
	}
	send := func() {
		t.Helper()
		select {
		case tick <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("no prober takes the next period")
		}
	}
	waitFor(true)
	if rec := do(http.MethodGet, "/api/overview", ""); rec.Code != http.StatusOK {
		t.Fatalf("rpcd down: a loopback read passes: %d", rec.Code)
	}
	// Still down at the next period: the door stays as it is.
	send()
	send() // the second send waits until the first probe is done
	if !g.NoRPC() {
		t.Fatal("a probe that failed cleared the no-rpcd state")
	}
	f.setErr(nil)
	send()
	waitFor(false)
	mu.Lock()
	if len(periods) == 0 || periods[0] != 30*time.Second {
		t.Errorf("the prober asks every 30 s: %v", periods)
	}
	mu.Unlock()
	if rec := do(http.MethodGet, "/api/overview", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("rpcd back: a loopback read without a session: %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/api/login", `{"user":"root","password":"pw"}`); rec.Code != http.StatusOK {
		t.Errorf("rpcd back: a login: %d %s", rec.Code, rec.Body)
	}
	// Back up, the prober has ended: nobody takes another tick.
	select {
	case tick <- time.Now():
		t.Error("the prober runs on after rpcd answered")
	case <-time.After(100 * time.Millisecond):
	}

	// A house-side listen that gave way to loopback is asked again too.
	f3 := &fakeRPC{err: down}
	g3 := c.newGate(f3, "")
	if got := c.armListen("192.168.50.1:8788", g3, func(time.Duration) {}, stop); got != "127.0.0.1:8788" || !g3.NoRPC() {
		t.Fatalf("house-side listen without rpcd: %q, %v", got, g3.NoRPC())
	}
	f3.setErr(nil)
	send()
	for i := 0; i < 400 && g3.NoRPC(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if g3.NoRPC() {
		t.Error("the loopback fallback of a house-side listen never learned rpcd came back")
	}

	// And it ends with serve while rpcd is still down.
	f2 := &fakeRPC{err: down}
	g2 := c.newGate(f2, "")
	stop2 := make(chan struct{})
	c.armListen("127.0.0.1:8788", g2, func(time.Duration) {}, stop2)
	for i := 0; i < 400 && !g2.NoRPC(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop2)
	time.Sleep(20 * time.Millisecond)
	select {
	case tick <- time.Now():
		t.Error("the prober runs on after serve stopped")
	case <-time.After(100 * time.Millisecond):
	}
}

// serve's gate knows the house from the console's own networks: a house
// device rotating its IPv6 addresses (the house's prefix, or link-local,
// which every device shares as fe80::/64) never locks a neighbour out, and
// a /64 outside the house shares ten attempts.
func TestHouseIPv6LoginsAreCountedPerAddress(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	var h http.Handler
	fresh := func() {
		h = c.serveHandler(serveMux(c, func() export.Snapshot { return export.Snapshot{} }), c.newGate(&fakeRPC{}, ""))
	}
	login := func(remote, pass string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"root","password":"`+pass+`"}`))
		r.RemoteAddr, r.Host = remote, "192.168.50.1:8788"
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for _, d := range []struct{ pfx, zone, neighbour string }{
		{"2001:db8:50::", "", "[2001:db8:50::11]:1"},
		{"fe80::", "%br-lan", "[fe80::2%br-lan]:1"},
	} {
		fresh()
		for i := 0; i < 100; i++ {
			src := fmt.Sprintf("[%sa:%x%s]:1", d.pfx, i/5, d.zone)
			if code := login(src, "wrong"); code != http.StatusUnauthorized {
				t.Fatalf("%s, attempt %d of its own five: %d", src, i%5+1, code)
			}
		}
		if code := login(d.neighbour, "pw"); code != http.StatusOK {
			t.Errorf("%s after a house device of its /64 made 100 attempts across addresses: %d", d.neighbour, code)
		}
	}
	fresh()
	for i := 1; i <= 10; i++ {
		if code := login(fmt.Sprintf("[2001:db8:60::%x]:1", i), "wrong"); code != http.StatusUnauthorized {
			t.Errorf("outside address %d of its /64: %d", i, code)
		}
	}
	if code := login("[2001:db8:60::ff]:1", "pw"); code != http.StatusTooManyRequests {
		t.Errorf("an 11th address of a /64 outside the house: %d", code)
	}
}

// The browser can only use an IANA name: the zone flag's, UTC when unset, and
// never "Local" (the process's own zone, which a browser cannot read).
func TestTheGateNamesAZoneABrowserKnows(t *testing.T) {
	nairobi, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		t.Skip("no tzdata")
	}
	for _, c := range []struct {
		loc  *time.Location
		want string
	}{{nil, "UTC"}, {time.UTC, "UTC"}, {time.Local, "UTC"}, {nairobi, "Africa/Nairobi"}} {
		o := testOptions(t)
		o.Loc = c.loc
		con, err := buildConsole(o)
		if err != nil {
			t.Fatal(err)
		}
		if got := con.newGate(&fakeRPC{}, "").Zone; got != c.want {
			t.Errorf("Loc %v: the gate says %q, want %q", c.loc, got, c.want)
		}
	}
}
