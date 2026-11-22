# Runs ON flint2 with python3 -I: one MCP tool call to openwrt-mcp on
# 127.0.0.1:8730 as the client whose bearer token is in /root/nxlab.token.
# A uci_apply's confirm token is kept in /root/nxlab.confirm and printed as
# (token kept); "@confirm" as a token value reads it back. Tokens are never printed.
#   python3 -I mcpcall.py <tool> '<json arguments>'
import json, re, sys, urllib.request

TOK = open("/root/nxlab.token").read().strip()
URL = "http://127.0.0.1:8730/mcp"
sid = None


def rpc(method, params, rid=None):
    global sid
    body = {"jsonrpc": "2.0", "method": method, "params": params}
    if rid is not None:
        body["id"] = rid
    req = urllib.request.Request(URL, data=json.dumps(body).encode(), method="POST")
    req.add_header("Authorization", "Bearer " + TOK)
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    if sid:
        req.add_header("Mcp-Session-Id", sid)
    with urllib.request.urlopen(req, timeout=120) as r:
        sid = r.headers.get("Mcp-Session-Id") or sid
        raw = r.read().decode()
    if rid is None:
        return None
    if raw.lstrip().startswith("{"):
        return json.loads(raw)
    for line in raw.splitlines():
        if line.startswith("data:"):
            d = json.loads(line[5:])
            if d.get("id") == rid:
                return d
    return {"raw": raw}


tool, args = sys.argv[1], json.loads(sys.argv[2])
if args.get("token") == "@confirm":
    args["token"] = open("/root/nxlab.confirm").read().strip()
rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                   "clientInfo": {"name": "netryx-lab", "version": "0"}}, 1)
rpc("notifications/initialized", {})
res = rpc("tools/call", {"name": tool, "arguments": args}, 2)
text = json.dumps(res)
m = re.search(r'uci_confirm \{\\+"token\\+": \\+"([^"\\]+)', text)
if m:
    with open("/root/nxlab.confirm", "w") as f:
        f.write(m.group(1))
    text = text.replace(m.group(1), "(token kept)")
if "token" in args:
    text = text.replace(args["token"], "(token)")
text = text.replace(TOK, "(bearer)")
r = res.get("result", {})
if "content" in r:
    print("isError=%s" % r.get("isError", False))
    for c in r["content"]:
        t = c.get("text", "")
        if m:
            t = t.replace(m.group(1), "(token kept)")
        if "token" in args:
            t = t.replace(args["token"], "(token)")
        print(t.replace(TOK, "(bearer)"))
else:
    print(text)
