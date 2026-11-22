package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// rpcdMaxArgs bounds what a call may send on stdin: one MAC needs a few dozen
// bytes.
const rpcdMaxArgs = 4096

// rpcdMaxAnswer bounds what a call answers. ubusd refuses a message over
// 1 MiB, and blobmsg carries small JSON fields in up to twice their bytes, so
// a larger answer is an error object here rather than a message ubusd drops.
// openwrt-mcp cuts what it hands the model at 64 KiB in any case.
const rpcdMaxAnswer = 256 << 10

// rpcdKeys are the reads whose list is answered as an object keyed by each
// row's own id: openwrt-mcp cuts every array of an answer over 8 KiB to 16
// elements, and keeps every key of an object, so a house of more than 16
// devices or rules stays whole for the agent.
var rpcdKeys = map[string]string{"devices": "mac", "rules": "id"}

// rpcdMethods are the ubus methods of the object `netryx`, each one of the
// six reads of `netryx ctl`, with the arguments rpcd is told it takes.
var rpcdMethods = map[string]map[string]string{
	"status": {}, "devices": {}, "rules": {}, "alerts": {},
	"apps": {"mac": "str"}, "usage": {"mac": "str"},
}

// runRpcd is `netryx rpcd`, the rpcd exec plugin behind the ubus object
// `netryx` (/usr/libexec/rpcd/netryx runs it): `list` names the methods and
// `call <method>` reads its arguments on stdin and answers one JSON object.
// It is the router agent's read through openwrt-mcp's ubus_call, which needs
// no exec. Every method is a read of the root-only socket; a refusal or a
// failure is an object {"error": ...} with exit 0, so the call succeeds and
// the agent reads why in words rather than a bare ubus status.
func runRpcd(sock string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "list" {
		json.NewEncoder(stdout).Encode(rpcdMethods)
		return 0
	}
	answer, err := rpcdCall(sock, args, stdin)
	if err != nil {
		json.NewEncoder(stdout).Encode(map[string]string{"error": err.Error()})
		return 0
	}
	stdout.Write(answer)
	return 0
}

func rpcdCall(sock string, args []string, stdin io.Reader) ([]byte, error) {
	if len(args) != 2 || args[0] != "call" {
		return nil, fmt.Errorf("usage: netryx rpcd list | call <method>")
	}
	method := args[1]
	sig, ok := rpcdMethods[method]
	if !ok {
		return nil, fmt.Errorf("no method %q: the methods are status, devices, rules, alerts, apps and usage", method)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, rpcdMaxArgs+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > rpcdMaxArgs {
		return nil, fmt.Errorf("the arguments are larger than %d bytes", rpcdMaxArgs)
	}
	in := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("the arguments are not a JSON object: %v", err)
		}
	}
	for k, v := range in {
		if _, ok := sig[k]; !ok {
			return nil, fmt.Errorf("%s takes no argument %q", method, k)
		}
		if _, ok := v.(string); !ok {
			return nil, fmt.Errorf("%s: %s must be a string", method, k)
		}
	}
	// The ctl words follow the signature's order, never the map's.
	names := make([]string, 0, len(sig))
	for k := range sig {
		names = append(names, k)
	}
	sort.Strings(names)
	ctl := []string{method}
	for _, k := range names {
		if v, ok := in[k]; ok {
			ctl = append(ctl, v.(string))
		}
	}
	path, ok := ctlPath(ctl)
	if !ok {
		if len(sig) > 0 {
			return nil, fmt.Errorf("%s needs mac, a device's hardware address such as 02:4e:58:00:00:03", method)
		}
		return nil, fmt.Errorf("%s takes no arguments", method)
	}
	body, err := ctlFetch(sock, path)
	if err != nil {
		return nil, err
	}
	var answer json.RawMessage
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("the service's answer is not JSON: %v", err)
	}
	if key, ok := rpcdKeys[method]; ok {
		if answer, err = keyed(answer, key); err != nil {
			return nil, fmt.Errorf("%s: %v", method, err)
		}
	}
	out, err := json.Marshal(map[string]json.RawMessage{method: answer})
	if err != nil {
		return nil, err
	}
	if len(out) > rpcdMaxAnswer {
		return nil, fmt.Errorf("the %s answer is %d bytes, more than ubus carries here (%d)", method, len(out), rpcdMaxAnswer)
	}
	return append(out, '\n'), nil
}

// keyed turns a list of objects into one object keyed by each row's key field,
// the row itself unchanged.
func keyed(list json.RawMessage, key string) (json.RawMessage, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(list, &rows); err != nil {
		return nil, fmt.Errorf("the service's answer is not a list of objects: %v", err)
	}
	out := make(map[string]json.RawMessage, len(rows))
	for _, r := range rows {
		var k string
		if err := json.Unmarshal(r[key], &k); err != nil || k == "" {
			return nil, fmt.Errorf("a row has no %s", key)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("two rows share the %s %q", key, k)
		}
		b, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		out[k] = b
	}
	return json.Marshal(out)
}
