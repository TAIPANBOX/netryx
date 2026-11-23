package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"time"

	"netryx/internal/handoff"
	"netryx/internal/ramfile"
)

// consoleStateFile is where serve says how it actually listens, for the LuCI
// page: in RAM, private to root, written once the address and TLS are decided.
const consoleStateFile = "/tmp/netryx/console.json"

// consoleState is what serve writes there.
type consoleState struct {
	Listen string `json:"listen"`
	HTTPS  bool   `json:"https"`
}

// writeConsoleState records how serve listens; the LuCI page reads it through
// the plugin to build the console's address.
func writeConsoleState(path, listen string, https bool) error {
	b, err := json.Marshal(consoleState{Listen: listen, HTTPS: https})
	if err != nil {
		return err
	}
	return ramfile.Write(path, b)
}

// luciDeps is what `netryx rpcd-luci` reads: the pending codes' directory,
// serve's state file, the clock, and rpcd's answer to whose a session is.
type luciDeps struct {
	Dir, State string
	Now        func() time.Time
	User       func(sid string) (string, error)
}

// sessionUser asks rpcd, over the local ubus, which user a session signed in
// as: rpcd keeps it in the session's values at login.
func sessionUser(sid string) (string, error) {
	args, _ := json.Marshal(map[string]any{"ubus_rpc_session": sid, "keys": []string{"username"}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ubus", "call", "session", "get", string(args)).Output()
	if err != nil {
		return "", errors.New("rpcd knows no such session")
	}
	var v struct {
		Values struct {
			Username string `json:"username"`
		} `json:"values"`
	}
	if json.Unmarshal(out, &v) != nil || v.Values.Username == "" {
		return "", errors.New("the session carries no user")
	}
	return v.Values.Username, nil
}

// runRpcdLuci is `netryx rpcd-luci`, the rpcd exec plugin behind the ubus
// object luci.netryx (/usr/libexec/rpcd/luci.netryx runs it), which LuCI's
// "Network console" page calls. Its one method, handoff, mints a code for the
// caller's own rpcd session: uhttpd puts that session into the arguments
// itself, replacing any the page sent. The object is named apart from
// netryx, so the agent's grant for netryx.* never reaches it.
func runRpcdLuci(d luciDeps, args []string, stdin io.Reader, stdout io.Writer) int {
	if len(args) == 1 && args[0] == "list" {
		json.NewEncoder(stdout).Encode(map[string]any{"handoff": map[string]string{"nonce": "str"}})
		return 0
	}
	v, err := luciCall(d, args, stdin)
	if err != nil {
		json.NewEncoder(stdout).Encode(map[string]string{"error": err.Error()})
		return 0
	}
	json.NewEncoder(stdout).Encode(v)
	return 0
}

func luciCall(d luciDeps, args []string, stdin io.Reader) (map[string]any, error) {
	if len(args) != 2 || args[0] != "call" || args[1] != "handoff" {
		return nil, errors.New("the only method is handoff")
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, rpcdMaxArgs+1))
	if err != nil || len(raw) > rpcdMaxArgs {
		return nil, errors.New("the arguments cannot be read")
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &in) != nil {
		return nil, errors.New("the arguments are not a JSON object")
	}
	var sid, nonce string
	for k, v := range in {
		var dst *string
		switch k {
		case "ubus_rpc_session":
			dst = &sid
		case "nonce":
			dst = &nonce
		default:
			return nil, fmt.Errorf("handoff takes no argument %q", k)
		}
		if json.Unmarshal(v, dst) != nil {
			return nil, fmt.Errorf("%s is not a string", k)
		}
	}
	if !validSID(sid) {
		return nil, errors.New("handoff is called from a signed-in LuCI page")
	}
	// The nonce the page put in the browser's cookie: the code opens only there.
	if !validSID(nonce) {
		return nil, errors.New("handoff needs the page's nonce, 32 hex digits")
	}
	b, why := ramfile.Read(d.State, 4096)
	var st consoleState
	if b == nil || json.Unmarshal(b, &st) != nil {
		if why == "" {
			why = "it says nothing"
		}
		return nil, fmt.Errorf("the console is not running (%s)", why)
	}
	_, portS, err := net.SplitHostPort(st.Listen)
	port, perr := strconv.Atoi(portS)
	if err != nil || perr != nil {
		return nil, errors.New("the console's address cannot be read")
	}
	scheme := "http"
	if st.HTTPS {
		scheme = "https"
	}
	out := map[string]any{"scheme": scheme, "port": port, "loopback": loopbackListen(st.Listen)}
	if loopbackListen(st.Listen) {
		// Nothing in the house could open it: no code is minted for nothing.
		return out, nil
	}
	user, err := d.User(sid)
	if err != nil {
		return nil, err
	}
	code, err := handoff.Mint(d.Dir, sid, user, nonce, d.Now())
	if err != nil {
		return nil, err
	}
	out["code"] = code
	return out, nil
}

// validSID reports the shape of an rpcd session id: 32 lower-case hex digits.
func validSID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
