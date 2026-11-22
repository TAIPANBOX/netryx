// Package icons keeps the favicons of brands that have no freely licensed
// logo. The console promises the browser loads nothing from outside the
// router, so the router itself fetches each brand's icon once, keeps it in its
// state directory and serves it from there.
//
// Everything that arrives from the network is treated as hostile: the router
// is the one machine in the house that must not be talked into fetching from,
// or storing for the owner's browser, something other than a small raster
// image from the brand's own site. Hence https only, the brand's host or its
// subdomains only, a hard size cap, and a type decided by the file's magic
// bytes, never by what the server claims. SVG is refused outright: it is a
// document that can carry script, and the console would be serving it from
// its own origin.
package icons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// DefaultMaxBytes is what a touch icon or favicon needs with room to
	// spare; anything bigger is not an icon.
	DefaultMaxBytes = 64 << 10
	// DefaultRetry keeps a site with no usable icon from being asked daily.
	DefaultRetry = 7 * 24 * time.Hour

	maxRedirects = 3
	failuresName = "failures.json"
	// failuresStem names failures.json's temp files; the leading underscore
	// keeps them apart from every icon's, since no id starts with one.
	failuresStem = "_failures"
	// maxSide is far beyond any touch icon (180 px) and keeps a small file
	// from claiming a huge canvas for whatever later decodes it.
	maxSide = 512
	// failuresCap bounds what is read back: the list holds one small entry
	// per app, so a file this size is not ours.
	failuresCap = 1 << 20
)

// The paths tried, best first: the touch icon is usually 180 px PNG and looks
// right on a tile, favicon.ico is the fallback nearly every site still has.
var paths = []string{"/apple-touch-icon.png", "/favicon.ico"}

// Ids become file names, so the pattern admits no separator and no leading
// dot; the length cap keeps names well under any filesystem's limit.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// kind is one accepted image format. The content type served later is the
// kind's, chosen from the stored extension, which was chosen from the magic.
// config, where the standard library has a decoder, reads the header for the
// canvas size; ICO and WebP have none in it and are bounded by length only.
type kind struct {
	ext, contentType string
	is               func([]byte) bool
	config           func(io.Reader) (image.Config, error)
}

var kinds = []kind{
	{"png", "image/png", func(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }, png.DecodeConfig},
	{"ico", "image/x-icon", func(b []byte) bool { return bytes.HasPrefix(b, []byte("\x00\x00\x01\x00")) }, nil},
	{"jpg", "image/jpeg", func(b []byte) bool { return bytes.HasPrefix(b, []byte("\xff\xd8\xff")) }, jpeg.DecodeConfig},
	{"gif", "image/gif", func(b []byte) bool {
		return bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
	}, gif.DecodeConfig},
	{"webp", "image/webp", func(b []byte) bool {
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
	}, nil},
}

// check decides whether b is an icon worth keeping: a known raster format by
// its magic and, where the header can be read, a sane canvas.
func check(b []byte) (kind, error) {
	k, ok := sniff(b)
	if !ok {
		return kind{}, errors.New("not a PNG, ICO, JPEG, GIF or WebP image")
	}
	if k.config != nil {
		cfg, err := k.config(bytes.NewReader(b))
		if err != nil {
			return kind{}, fmt.Errorf("unreadable %s header: %w", k.ext, err)
		}
		if cfg.Width > maxSide || cfg.Height > maxSide {
			return kind{}, fmt.Errorf("%dx%d px, limit %d", cfg.Width, cfg.Height, maxSide)
		}
	}
	return k, nil
}

func sniff(b []byte) (kind, bool) {
	for _, k := range kinds {
		if k.is(b) {
			return k, true
		}
	}
	return kind{}, false
}

// Fetcher downloads and keeps favicons. Calls to Ensure are serialised by a
// mutex (the wiring runs them one after another anyway); Open takes no lock
// and may run concurrently with Ensure, because a file only ever appears
// under its final name by rename, whole.
type Fetcher struct {
	Dir string // <state>/icons
	// Client fetches; nil means NewClient(DefaultTimeout), which refuses
	// private addresses. Tests inject a client that reaches a local server.
	Client   *http.Client
	MaxBytes int64         // DefaultMaxBytes when zero
	Retry    time.Duration // DefaultRetry when zero
	Now      func() time.Time
	// Logf reports what is worth knowing but not worth failing for, such as
	// an unreadable failures.json. Nil means silent.
	Logf func(format string, args ...any)

	mu sync.Mutex
	// held is the failure list as this process last knew it. It is merged
	// into what is read from disk, so a list that cannot be saved (a full
	// overlay) or read still keeps the week's wait for as long as we run.
	held map[string]int64
}

func (f *Fetcher) maxBytes() int64 {
	if f.MaxBytes > 0 {
		return f.MaxBytes
	}
	return DefaultMaxBytes
}

func (f *Fetcher) retry() time.Duration {
	if f.Retry > 0 {
		return f.Retry
	}
	return DefaultRetry
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fetcher) logf(format string, args ...any) {
	if f.Logf != nil {
		f.Logf(format, args...)
	}
}

func validID(id string) bool { return idPattern.MatchString(id) }

// validDomain admits a plain DNS name of at least two labels. No scheme, port,
// path or user info can pass, nor an IP literal: the catalog names brands, and
// an address would let a bad entry point the router at the LAN.
func validDomain(d string) bool {
	if len(d) == 0 || len(d) > 253 || net.ParseIP(d) != nil {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !labelPattern.MatchString(l) {
			return false
		}
	}
	// A top-level label of digits only would make 10.0.0.1-like names
	// syntactically valid; real TLDs always carry a letter.
	return strings.ContainsAny(labels[len(labels)-1], "abcdefghijklmnopqrstuvwxyz")
}

// Ensure makes sure the icon for id is on disk, fetching it from domain if it
// is not. It does nothing when the icon is already kept or when a failure for
// id was recorded less than Retry ago. A failed fetch is recorded, except when
// ctx was cancelled (a shutdown says nothing about the site) or the failure
// was the network's (ErrNetwork: no name resolution, no route, no answer in
// time), which is returned for the caller to act on.
func (f *Fetcher) Ensure(ctx context.Context, id, domain string) error {
	if !validID(id) {
		return fmt.Errorf("icons: refusing id %q", id)
	}
	domain = strings.ToLower(domain)
	if !validDomain(domain) {
		return fmt.Errorf("icons: refusing domain %q", domain)
	}
	if f.Dir == "" {
		return errors.New("icons: no directory")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.prepareDir(); err != nil {
		return err
	}
	if _, _, _, ok := f.find(id); ok {
		return nil
	}
	now := f.now()
	fails := f.loadFailures()
	for held, at := range f.held {
		if at > fails[held] {
			fails[held] = at
		}
	}
	if at, ok := fails[id]; ok && f.recent(at, now) {
		return nil
	}
	f.removeStale(id)

	body, k, err := f.download(ctx, domain)
	if err == nil {
		err = writeAtomic(f.Dir, id, filepath.Join(f.Dir, id+"."+k.ext), body)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, ErrNetwork) {
			return err
		}
		fails[id] = now.Unix()
		f.prune(fails, now)
		if werr := f.saveFailures(fails); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	if _, had := fails[id]; had {
		delete(fails, id)
		f.prune(fails, now)
		return f.saveFailures(fails)
	}
	return nil
}

// prepareDir makes the icon directory exist as a real directory that only
// netryx can read. A symlink in its place is refused, not followed: the
// files written there are served to the owner's browser.
func (f *Fetcher) prepareDir() error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return fmt.Errorf("icons: %w", err)
	}
	fi, err := os.Lstat(f.Dir)
	if err != nil {
		return fmt.Errorf("icons: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("icons: %s is not a directory", f.Dir)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(f.Dir, 0o700); err != nil {
			return fmt.Errorf("icons: %w", err)
		}
	}
	return nil
}

// dirIsReal says whether Dir is a directory and not a link to one.
func (f *Fetcher) dirIsReal() bool {
	fi, err := os.Lstat(f.Dir)
	return err == nil && fi.IsDir()
}

// recent says whether a failure at unix time at still holds. A failure stamped
// in the future means the clock moved backwards (a router without an RTC boots
// in the past until NTP answers), and is not trusted to hold anything back.
func (f *Fetcher) recent(at int64, now time.Time) bool {
	age := now.Sub(time.Unix(at, 0))
	return age >= 0 && age < f.retry()
}

// prune drops failures that no longer hold, so the list stays one entry per
// currently failing app.
func (f *Fetcher) prune(fails map[string]int64, now time.Time) {
	for id, at := range fails {
		if !f.recent(at, now) {
			delete(fails, id)
		}
	}
}

func (f *Fetcher) download(ctx context.Context, domain string) ([]byte, kind, error) {
	// A copy, so the caller's client keeps its own policy; no cookie jar, so
	// one brand's site cannot plant state that rides along to the next.
	c := *f.client()
	c.Jar = nil
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("more than %d redirects", maxRedirects)
		}
		return allowed(req.URL, domain)
	}
	var errs []error
	for _, p := range paths {
		b, k, err := f.get(ctx, &c, "https://"+domain+p)
		if err == nil {
			return b, k, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	// Only when every attempt failed for the network's reasons is the fetch
	// the network's: a site that answered one URL has answered.
	all := errors.Join(errs...)
	for _, e := range errs {
		if !networkError(e) {
			return nil, kind{}, fmt.Errorf("icons: %s: %w", domain, all)
		}
	}
	return nil, kind{}, fmt.Errorf("%w: %s: %w", ErrNetwork, domain, all)
}

// allowed is the redirect rule: https on the default port, to the brand's own
// host or a subdomain of it. The dot in the suffix check is what keeps
// evilbrand.test from passing for brand.test.
func allowed(u *url.URL, domain string) error {
	if u.Scheme != "https" {
		return fmt.Errorf("redirect to %s:// refused", u.Scheme)
	}
	if u.User != nil {
		return errors.New("redirect with user info refused")
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("redirect to port %s refused", p)
	}
	h := strings.ToLower(u.Hostname())
	if h != domain && !strings.HasSuffix(h, "."+domain) {
		return fmt.Errorf("redirect to %s refused", h)
	}
	return nil
}

func (f *Fetcher) get(ctx context.Context, c *http.Client, u string) ([]byte, kind, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, kind{}, err
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", "netryx")
	// Whether the line brought a connection (TLS included) before anything
	// failed: a deadline after that is the site saying nothing, not the
	// network being absent.
	var connected atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	}))
	resp, err := c.Do(req)
	if err != nil {
		return nil, kind{}, afterConnect(u, err, connected.Load())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, kind{}, fmt.Errorf("%s: status %d", u, resp.StatusCode)
	}
	max := f.maxBytes()
	if resp.ContentLength > max {
		return nil, kind{}, fmt.Errorf("%s: %d bytes announced, limit %d", u, resp.ContentLength, max)
	}
	// One byte past the limit is read so that a body of exactly the limit and
	// one that goes on beyond it can be told apart.
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, kind{}, afterConnect(u, err, true)
	}
	if int64(len(b)) > max {
		return nil, kind{}, fmt.Errorf("%s: more than %d bytes", u, max)
	}
	if len(b) == 0 {
		return nil, kind{}, fmt.Errorf("%s: empty body", u)
	}
	k, err := check(b)
	if err != nil {
		return nil, kind{}, fmt.Errorf("%s: %w", u, err)
	}
	return b, k, nil
}

// find locates the kept file for id. Lstat, not Stat: a symlink or anything
// but a regular file under an icon's name is not an icon and is not followed.
func (f *Fetcher) find(id string) (string, kind, fs.FileInfo, bool) {
	for _, k := range kinds {
		p := filepath.Join(f.Dir, id+"."+k.ext)
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if !fi.Mode().IsRegular() {
			return "", kind{}, nil, false
		}
		return p, k, fi, true
	}
	return "", kind{}, nil, false
}

// Open returns the kept icon for id, read whole into memory: it is at most
// MaxBytes, and a reader over bytes leaves the caller no file to close. The
// content type follows the stored extension, and the bytes must still carry
// that format's magic, so a file planted in the directory under an icon's
// name is not served as one.
func (f *Fetcher) Open(id string) (r io.ReadSeeker, contentType string, modTime time.Time, ok bool) {
	if !validID(id) || f.Dir == "" || !f.dirIsReal() {
		return nil, "", time.Time{}, false
	}
	p, k, fi, ok := f.find(id)
	if !ok {
		return nil, "", time.Time{}, false
	}
	fh, err := openIcon(p)
	if err != nil {
		return nil, "", time.Time{}, false
	}
	defer fh.Close()
	// The name may have been swapped for a link between Lstat and Open; the
	// opened file must be the very one that was checked.
	st, err := fh.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(fi, st) {
		return nil, "", time.Time{}, false
	}
	max := f.maxBytes()
	b, err := io.ReadAll(io.LimitReader(fh, max+1))
	if err != nil || len(b) == 0 || int64(len(b)) > max {
		return nil, "", time.Time{}, false
	}
	if got, err := check(b); err != nil || got.ext != k.ext {
		return nil, "", time.Time{}, false
	}
	return bytes.NewReader(b), k.contentType, st.ModTime(), true
}

func (f *Fetcher) loadFailures() map[string]int64 {
	path := filepath.Join(f.Dir, failuresName)
	fh, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]int64{}
	}
	if err != nil {
		f.logf("icons: %s unreadable, starting afresh: %v", path, err)
		return map[string]int64{}
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, failuresCap+1))
	if err == nil && len(b) > failuresCap {
		err = errors.New("too large")
	}
	m := map[string]int64{}
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		f.logf("icons: %s unreadable, starting afresh: %v", path, err)
		return map[string]int64{}
	}
	for id := range m {
		if !validID(id) {
			delete(m, id)
		}
	}
	return m
}

// saveFailures writes the list and, whatever happens to the write, keeps it
// as this process's own copy.
func (f *Fetcher) saveFailures(m map[string]int64) error {
	f.held = make(map[string]int64, len(m))
	for id, at := range m {
		f.held[id] = at
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeAtomic(f.Dir, failuresStem, filepath.Join(f.Dir, failuresName), b)
}

// tempPattern names temp files so they can never be taken for an icon: they
// start with a dot, which no valid id does, and end in .tmp. CreateTemp puts
// decimal digits where the star is.
func tempPattern(stem string) string { return "." + stem + ".*.tmp" }

// staleTemp says whether name is a temp file of id exactly. A glob would not
// do: ".a.*.tmp" also matches ".a.b.123.tmp", which belongs to id "a.b". Ids
// may contain dots but the random part never does, so the part between the
// id and ".tmp" must be digits only.
func staleTemp(name, id string) bool {
	prefix := "." + id + "."
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tmp") ||
		len(name) <= len(prefix)+len(".tmp") {
		return false
	}
	for _, c := range name[len(prefix) : len(name)-len(".tmp")] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// removeStale clears temp files a crash may have left for id, so they do not
// pile up across restarts.
func (f *Fetcher) removeStale(id string) {
	ents, _ := os.ReadDir(f.Dir)
	for _, e := range ents {
		if staleTemp(e.Name(), id) {
			os.Remove(filepath.Join(f.Dir, e.Name()))
		}
	}
}

// writeAtomic puts data at final by temp file, fsync and rename, so a reader
// sees either nothing or the whole file, and a power cut leaves no torn icon.
// CreateTemp makes the file 0600.
func writeAtomic(dir, stem, final string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, tempPattern(stem))
	if err != nil {
		return fmt.Errorf("icons: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("icons: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("icons: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("icons: %w", err)
	}
	if err = os.Rename(name, final); err != nil {
		return fmt.Errorf("icons: %w", err)
	}
	// The rename is durable only once the directory is synced; failing to
	// sync it leaves a good file that may vanish in a power cut, which the
	// next Ensure repairs, so it is not an error.
	if d, derr := os.Open(dir); derr == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// DefaultTimeout bounds one fetch from the client a Fetcher falls back to.
const DefaultTimeout = 10 * time.Second

var errNotPublic = errors.New("icons: not a public address")

// Ranges netip has no predicate for. 100.64.0.0/10 is carrier-grade NAT and
// also where Tailscale puts its peers; 0.0.0.0/8 reaches the local host on
// Linux; 240.0.0.0/4 is reserved and holds the broadcast address.
var notPublic4 = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// IPv6 ranges that are never a brand's site: Teredo tunnels, IPv4-compatible
// and site-local addresses (both deprecated), the discard prefix and the
// documentation prefix.
var notPublic6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// Prefixes that carry an IPv4 address inside. They are judged by that
// address, not refused whole: an IPv6-only line with DNS64 reaches every
// IPv4 brand through NAT64.
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour      = netip.MustParsePrefix("2002::/16")
)

func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	b := ip.As16()
	switch {
	case nat64WellKnown.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case nat64Local.Contains(ip):
		// RFC 8215 leaves the layout to the operator. The common /96 layout
		// leaves bytes 6 to 11 zero and puts the address last; otherwise it
		// is RFC 6052's /48 layout, bytes 6, 7, 9 and 10 (byte 8 is reserved).
		if [6]byte(b[6:12]) == [6]byte{} {
			return netip.AddrFrom4([4]byte(b[12:16])), true
		}
		return netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

func publicAddr(ip netip.Addr) bool {
	// An IPv4 address written inside IPv6 is the IPv4 address.
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	if ip.Is6() {
		for _, p := range notPublic6 {
			if p.Contains(ip) {
				return false
			}
		}
		if v4, ok := embeddedIPv4(ip); ok {
			return publicAddr(v4)
		}
		return true
	}
	for _, p := range notPublic4 {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// guardDial runs as net.Dialer.Control, after DNS has answered and just
// before the socket connects, so it judges the address actually dialled. A
// brand's name, or a subdomain a redirect leads to, that resolves into the
// house would otherwise point the router at its own LAN; certificate checks
// make that hard to exploit, and this makes it impossible.
func guardDial(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", errNotPublic, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip) {
		return fmt.Errorf("%w: %s", errNotPublic, host)
	}
	return nil
}

// NewClient is the client the router fetches icons with: every connection
// goes through guardDial, no proxy is taken from the environment, and the
// whole exchange, redirects included, is bounded by timeout. A Fetcher with
// no Client uses one of these.
func NewClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: timeout, Control: guardDial}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			// An icon's response has a handful of headers; this keeps a
			// hostile server from making the router buffer a megabyte of them.
			MaxResponseHeaderBytes: 16 << 10,
			MaxIdleConns:           2,
			IdleConnTimeout:        30 * time.Second,
			ForceAttemptHTTP2:      true,
		},
	}
}

var defaultClient = NewClient(DefaultTimeout)

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return defaultClient
}

// errSiteSilent marks a site that took the connection and then failed in a
// way that would otherwise read as the network's, most often by saying
// nothing until the deadline (a geo block, a bot wall).
var errSiteSilent = errors.New("icons: the site took the connection and said nothing")

// afterConnect keeps err as it is unless the connection had been made, in
// which case a network-looking failure is the site's: the original error is
// kept as text only, so networkError cannot find a deadline in it.
func afterConnect(u string, err error, connected bool) error {
	if connected && networkError(err) {
		return fmt.Errorf("%s: %w: %v", u, errSiteSilent, err)
	}
	if connected {
		return fmt.Errorf("%s: %w", u, err)
	}
	return err
}

// ErrNetwork marks a fetch that failed because the network was not there: no
// answer from the resolver, no route, or no answer in time. It
// says nothing about the site, so it is not recorded as the site's failure: a
// router booting before its line is up would otherwise leave every brand
// without an icon for a week. The caller decides when to try again.
var ErrNetwork = errors.New("icons: the network, not the site")

// networkError says whether err is the network's rather than the site's. An
// address that is not public is the site's (its name points into the house),
// and so is everything the site answered.
func networkError(err error) bool {
	if errors.Is(err, errNotPublic) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// A name the DNS says does not exist, and a host that refused the
	// connection, are answers: the line was up to bring them, so they are the
	// site's. A dead domain is then recorded and asked again in a week, not
	// every day, and cannot end every pass.
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return !(dns.IsNotFound && !dns.IsTemporary)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Due says whether Ensure would ask the site for id now: the icon is not kept
// and no failure for it holds.
func (f *Fetcher) Due(id string) bool {
	if !validID(id) || f.Dir == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, _, _, ok := f.find(id); ok {
		return false
	}
	at, ok := f.held[id]
	if fa, has := f.loadFailures()[id]; has && fa > at {
		at, ok = fa, true
	}
	return !ok || !f.recent(at, f.now())
}
