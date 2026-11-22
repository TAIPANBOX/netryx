package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ctlSocket is where serve answers `netryx ctl`, and where ctl asks.
const ctlSocket = "/var/run/netryx.sock"

// ctlMaxAnswer bounds what ctl reads; a larger answer is an error, never a
// silently cut JSON.
const ctlMaxAnswer = 8 << 20

const ctlUsage = "usage: netryx ctl devices | rules | status | alerts | apps <mac> | usage <mac>"

// runCtl is `netryx ctl`: one read of the running service through its
// root-only socket, printed as the API's own JSON. The same six reads are the
// router agent's, as the ubus object netryx (runRpcd); it accepts exactly
// six forms and nothing else, and the agent's writes are UCI changes, never a
// call to the socket. It returns the exit status.
func runCtl(sock string, args []string, stdout, stderr io.Writer) int {
	path, ok := ctlPath(args)
	if !ok {
		fmt.Fprintln(stderr, ctlUsage)
		return 1
	}
	body, err := ctlFetch(sock, path)
	if err != nil {
		fmt.Fprintln(stderr, "netryx ctl:", err)
		return 1
	}
	stdout.Write(body)
	return 0
}

// ctlFetch is the one read of the socket that ctl and the ubus object make:
// the API's JSON for path, ending in a newline, or an error that says why.
func ctlFetch(sock, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, "http://netryx"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := socketClient(sock).Do(req)
	if err != nil {
		return nil, fmt.Errorf("the service does not answer on %s (is it running, and are you root?): %v", sock, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, ctlMaxAnswer+1))
	if err != nil {
		return nil, err
	}
	if len(body) > ctlMaxAnswer {
		return nil, fmt.Errorf("the answer is larger than %d bytes", ctlMaxAnswer)
	}
	if !strings.HasSuffix(string(body), "\n") {
		body = append(body, '\n')
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

var macForm = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// ctlPath maps the six forms to the API path they read.
func ctlPath(args []string) (string, bool) {
	switch {
	case len(args) == 1 && args[0] == "devices":
		return "/api/devices", true
	case len(args) == 1 && args[0] == "rules":
		return "/api/rules", true
	case len(args) == 1 && args[0] == "status":
		return "/api/overview", true
	case len(args) == 1 && args[0] == "alerts":
		return "/api/alerts", true
	case len(args) == 2 && (args[0] == "apps" || args[0] == "usage"):
		// Any spelling of a MAC becomes the lower-case colon form before it
		// goes into a path; anything else never reaches the socket.
		mac := strings.ToLower(strings.ReplaceAll(args[1], "-", ":"))
		if !macForm.MatchString(mac) {
			return "", false
		}
		if args[0] == "usage" {
			return "/api/usage?mac=" + mac, true
		}
		return "/api/devices/" + mac + "/apps?range=24h", true
	}
	return "", false
}

func socketClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
}

// socketRoute is what the socket answers: the six reads ctl makes.
var socketRoute = regexp.MustCompile(`^/api/(devices|rules|overview|usage|alerts|devices/[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}/apps)$`)

// usageQuery is the one query of the usage read: one MAC, nothing else.
var usageQuery = regexp.MustCompile(`^mac=[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// socketHandler lets the API answer only those reads. The socket sits behind
// no login and no Host check, since only root can open it, so its narrowness
// is the whole protection: every write, the session and every other view is
// refused here, whatever the API would have done with it.
func socketHandler(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !socketRoute.MatchString(r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not found"}`)
			return
		}
		// The one query ctl sends: apps for the last 24 hours, nothing else.
		if strings.HasSuffix(r.URL.Path, "/apps") && r.URL.RawQuery != "range=24h" ||
			r.URL.Path == "/api/usage" && !usageQuery.MatchString(r.URL.RawQuery) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not found"}`)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			io.WriteString(w, `{"error":"read only"}`)
			return
		}
		api.ServeHTTP(w, r)
	})
}

// umaskMu serialises the one place the process umask is changed.
var umaskMu sync.Mutex

// listenSocket listens on a unix socket that is mode 0600 from its first
// moment: the umask is narrowed around the bind, so there is no instant at
// which another user could connect. A
// socket left by a crash is replaced; anything else at the path is an error.
func listenSocket(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	// The umask is process-wide: for the instant of the bind any other file
	// created by this process gets the narrower mode too, so serve opens the
	// socket before any file writer starts. There is no chmod after the
	// bind: the file is never wider than 0600, not even briefly.
	umaskMu.Lock()
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	umaskMu.Unlock()
	return l, err
}

// serveSocket answers the reads of `netryx ctl` on path. The returned
// function stops it and removes the socket.
func (c *console) serveSocket(path string) (func(), error) {
	l, err := listenSocket(path)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           socketHandler(c.api),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
	}
	go srv.Serve(l)
	return func() {
		srv.Close()
		os.Remove(path)
	}, nil
}
