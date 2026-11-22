package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"syscall"
	"time"
)

// RPC is what netryx asks rpcd, through uhttpd's JSON-RPC endpoint on the
// router itself (docs/stage2-measured.md, section 2).
type RPC interface {
	// Login opens a session for user; ErrBadLogin when rpcd refuses it,
	// ErrNoRPC when nothing answers as rpcd.
	Login(ctx context.Context, user, pass string) (sid string, err error)
	// Access asks whether the session's ACL grants function on object in
	// scope; ErrSessionGone when rpcd does not know the session (unknown and
	// expired look the same).
	Access(ctx context.Context, sid, scope, object, function string) (bool, error)
	// Destroy ends the session.
	Destroy(ctx context.Context, sid string) error
}

var (
	// ErrBadLogin is a login rpcd refused: result[0] != 0 (6 for a wrong
	// user or password, 2 for a missing field) or an error object.
	ErrBadLogin = errors.New("auth: rpcd refused the login")
	// ErrNoRPC is an endpoint that does not answer as rpcd: nothing
	// listening, a timeout, an HTTP error, or an answer that is not
	// JSON-RPC.
	ErrNoRPC = errors.New("auth: rpcd does not answer")
	// ErrSessionGone is error -32002 ("Access denied") on a call with a
	// session id: the session is unknown or has expired.
	ErrSessionGone = errors.New("auth: the session is unknown or expired")
)

// nullSID is the session rpcd gives an unauthenticated caller.
const nullSID = "00000000000000000000000000000000"

const (
	codeAccessDenied = -32002
	// maxReply bounds what one answer may hand back: a login's ACL list
	// is about 7 KB with LuCI installed.
	maxReply = 1 << 20
)

// ValidSID reports whether s has the shape of an rpcd session id: 32
// lower-case hex digits, and not the null session.
func ValidSID(s string) bool {
	if len(s) != 32 || s == nullSID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type rpcClient struct {
	url string
	c   *http.Client
	id  atomic.Uint64
}

// NewRPC speaks to rpcd at url (uhttpd's ubus prefix, normally
// http://127.0.0.1/ubus). A nil client is one that dials loopback only, so a
// password never leaves the router whatever the URL or a redirect says.
func NewRPC(url string, c *http.Client) RPC {
	if c == nil {
		c = loopbackClient()
	}
	return &rpcClient{url: url, c: c}
}

// loopbackClient dials only loopback addresses, and follows uhttpd's
// redirect to https on the router itself (redirect_https): the certificate
// there is uhttpd's self-signed one, and a connection that cannot leave the
// machine has nobody in the middle to verify against. It never uses a proxy.
func loopbackClient() *http.Client {
	d := &net.Dialer{
		Timeout: 3 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !ap.Addr().Unmap().IsLoopback() {
				return fmt.Errorf("rpcd is asked on loopback only, not %s", address)
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 2 {
				return errors.New("more than two redirects")
			}
			return nil
		},
	}
}

type rpcReply struct {
	Result []json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *rpcReply) status() (int, error) {
	var st int
	if len(r.Result) == 0 || json.Unmarshal(r.Result[0], &st) != nil {
		return 0, errors.New("auth: rpcd answered no ubus status")
	}
	return st, nil
}

// call sends one JSON-RPC call. The arguments, a password among them,
// travel only in the body; no error carries them.
func (r *rpcClient) call(ctx context.Context, sid, object, method string, args any) (*rpcReply, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": r.id.Add(1), "method": "call",
		"params": []any{sid, object, method, args},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoRPC, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoRPC, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxReply))
		return nil, fmt.Errorf("%w: HTTP %d", ErrNoRPC, resp.StatusCode)
	}
	var rep rpcReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReply)).Decode(&rep); err != nil {
		return nil, fmt.Errorf("%w: not a JSON-RPC answer: %v", ErrNoRPC, err)
	}
	if rep.Error == nil && len(rep.Result) == 0 {
		return nil, fmt.Errorf("%w: an answer with neither result nor error", ErrNoRPC)
	}
	return &rep, nil
}

func (r *rpcClient) Login(ctx context.Context, user, pass string) (string, error) {
	rep, err := r.call(ctx, nullSID, "session", "login", map[string]string{"username": user, "password": pass})
	if err != nil {
		return "", err
	}
	if rep.Error != nil {
		return "", fmt.Errorf("%w (error %d)", ErrBadLogin, rep.Error.Code)
	}
	st, err := rep.status()
	if err != nil {
		return "", err
	}
	if st != 0 {
		return "", fmt.Errorf("%w (ubus status %d)", ErrBadLogin, st)
	}
	var ses struct {
		SID string `json:"ubus_rpc_session"`
	}
	if len(rep.Result) < 2 || json.Unmarshal(rep.Result[1], &ses) != nil || !ValidSID(ses.SID) {
		return "", errors.New("auth: rpcd's login answer carries no session id")
	}
	return ses.SID, nil
}

func (r *rpcClient) Access(ctx context.Context, sid, scope, object, function string) (bool, error) {
	if !ValidSID(sid) {
		return false, ErrSessionGone
	}
	rep, err := r.call(ctx, sid, "session", "access", map[string]string{"scope": scope, "object": object, "function": function})
	if err != nil {
		return false, err
	}
	if rep.Error != nil {
		if rep.Error.Code == codeAccessDenied {
			return false, ErrSessionGone
		}
		return false, fmt.Errorf("auth: rpcd error %d on access", rep.Error.Code)
	}
	st, err := rep.status()
	if err != nil {
		return false, err
	}
	if st != 0 {
		return false, fmt.Errorf("auth: ubus status %d on access", st)
	}
	var a struct {
		Access *bool `json:"access"`
	}
	if len(rep.Result) < 2 || json.Unmarshal(rep.Result[1], &a) != nil || a.Access == nil {
		return false, errors.New("auth: rpcd's access answer says neither yes nor no")
	}
	return *a.Access, nil
}

func (r *rpcClient) Destroy(ctx context.Context, sid string) error {
	if !ValidSID(sid) {
		return ErrSessionGone
	}
	rep, err := r.call(ctx, sid, "session", "destroy", map[string]string{})
	if err != nil {
		return err
	}
	if rep.Error != nil {
		if rep.Error.Code == codeAccessDenied {
			return ErrSessionGone
		}
		return fmt.Errorf("auth: rpcd error %d on destroy", rep.Error.Code)
	}
	if st, err := rep.status(); err != nil || st != 0 {
		return fmt.Errorf("auth: rpcd did not end the session (status %d, %v)", st, err)
	}
	return nil
}

// Probe tells whether rpcd answers, with a login carrying empty credentials:
// any JSON-RPC answer, a refusal included, proves it is there, and only a
// transport failure is ErrNoRPC. It never sends a password.
func Probe(ctx context.Context, r RPC) error {
	sid, err := r.Login(ctx, "", "")
	if err == nil {
		r.Destroy(ctx, sid)
		return nil
	}
	if errors.Is(err, ErrNoRPC) {
		return err
	}
	return nil
}
