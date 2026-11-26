package main

// The console's https: uhttpd's own certificate when the router has one (the
// one LuCI over https uses), otherwise a certificate of its own, made once and
// kept; and one port that answers both, so http://router:8788 from the house
// is sent to https on the same port, while the SSH tunnel (plain http from the
// router itself) keeps working.

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ownCertDir is where the console keeps a certificate of its own when uhttpd
// has none: beside its history, which a sysupgrade keeps.
const ownCertDir = "/etc/netryx"

// ownPair returns the console's own certificate and key in dir, made on the
// first start (ECDSA P-256, self-signed, ten years) and kept, so the browser
// that accepted it once is not asked again. The key is root's alone.
func ownPair(dir, listen string) (string, string, error) {
	crt, key := filepath.Join(dir, "console.crt"), filepath.Join(dir, "console.key")
	_, e1 := os.Stat(crt)
	_, e2 := os.Stat(key)
	if e1 == nil && e2 == nil {
		return crt, key, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	pk, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "netryx console"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{},
	}
	if host, _, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/hostname"); err == nil {
		if h := strings.TrimSpace(string(b)); h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h, h+".lan")
		}
	}
	tpl.IPAddresses = append(tpl.IPAddresses, net.IPv4(127, 0, 0, 1))
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &pk.PublicKey, pk)
	if err != nil {
		return "", "", err
	}
	kb, err := x509.MarshalECPrivateKey(pk)
	if err != nil {
		return "", "", err
	}
	// The key first, private from its first byte; each file whole, by rename.
	if err := writeWhole(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return "", "", err
	}
	if err := writeWhole(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", err
	}
	return crt, key, nil
}

// writeWhole writes a temporary file beside path with mode, then renames it
// over path, so a reader never sees half of it.
func writeWhole(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// peekTimeout is how long a new connection has to send its first byte before
// it is let go; a variable so a test can make it short.
var peekTimeout = 5 * time.Second

// splitTLS divides one listener into two by each connection's first byte: a
// TLS handshake (0x16) goes to the first, anything else to the second.
func splitTLS(ln net.Listener) (net.Listener, net.Listener) {
	t := &chanListener{addr: ln.Addr(), c: make(chan net.Conn), done: make(chan struct{})}
	p := &chanListener{addr: ln.Addr(), c: make(chan net.Conn), done: make(chan struct{})}
	// Read once here: the goroutines below never touch the variable, so a
	// test that shortens it and puts it back does not race them.
	timeout := peekTimeout
	go func() {
		defer t.Close()
		defer p.Close()
		var pause time.Duration
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				// Out of file descriptors, or any other error short of the
				// listener closing: wait and try again, as http.Server does,
				// so connections opened to exhaust the router cannot end the
				// console.
				if pause == 0 {
					pause = 5 * time.Millisecond
				} else if pause *= 2; pause > time.Second {
					pause = time.Second
				}
				time.Sleep(pause)
				continue
			}
			pause = 0
			go func() {
				conn.SetReadDeadline(time.Now().Add(timeout))
				br := bufio.NewReader(conn)
				first, err := br.Peek(1)
				if err != nil {
					conn.Close()
					return
				}
				conn.SetReadDeadline(time.Time{})
				to := p
				if first[0] == 0x16 {
					to = t
				}
				if !to.give(&peekedConn{Conn: conn, r: br}) {
					conn.Close()
				}
			}()
		}
	}()
	return t, p
}

type chanListener struct {
	addr net.Addr
	c    chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *chanListener) give(c net.Conn) bool {
	select {
	case l.c <- c:
		return true
	case <-l.done:
		return false
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.c:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

// peekedConn is a connection whose first bytes were read to choose its side.
type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// plainSide answers plain http on the https port: from the router itself (the
// SSH tunnel) it is the console as it is; from the house it is a permanent
// redirect to https on the same port, for one of the router's own names only,
// or, with the redirect turned off, a refusal that says to use https.
func (c *console) plainSide(full http.Handler, redirect func() bool, loopback func(remote string) bool) http.Handler {
	send := c.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !redirect() {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, "this console speaks https only: open it with https://\n")
			return
		}
		http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopback(r.RemoteAddr) {
			full.ServeHTTP(w, r)
			return
		}
		send.ServeHTTP(w, r)
	})
}

// remoteLoopback reports a request from the router itself.
func remoteLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
