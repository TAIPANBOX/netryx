// Sign in: the console's own login, which asks the router's LuCI users. It takes
// the page's place when there is no session, and gives it back at the same
// screen once the person is in.

import { h } from "preact";
import { useState } from "preact/hooks";
import { login } from "../api";
import { loginWarning } from "../rules";

export function Login({ onDone }: { onDone: (user: string, canWrite: boolean) => void }) {
  const [user, setUser] = useState("root");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: Event) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setErr("");
    const r = await login(user.trim(), password);
    setBusy(false);
    if (r.ok) {
      setPassword("");
      onDone(r.user, r.canWrite);
    } else {
      setErr(r.text);
      setPassword("");
    }
  };

  return (
    <div class="login-page">
      <form class="card login" onSubmit={submit} aria-label="Sign in">
        <h1>netryx</h1>
        <p class="soft">Sign in as in LuCI: the same user and password as on the router's own page.</p>
        {loginWarning(location.protocol) && <p class="banner" role="note">{loginWarning(location.protocol)}</p>}
        <label class="field">
          <span class="small muted">User</span>
          <input class="input" name="user" autocomplete="username" autocapitalize="none" spellcheck={false} value={user} onInput={(e) => setUser((e.target as HTMLInputElement).value)} required />
        </label>
        <label class="field">
          <span class="small muted">Password</span>
          <input class="input" name="password" type="password" autocomplete="current-password" value={password} onInput={(e) => setPassword((e.target as HTMLInputElement).value)} />
        </label>
        {err && <p class="banner" role="alert">{err}</p>}
        <button type="submit" class="btn btn-primary" disabled={busy || !user.trim()}>{busy ? "Signing in…" : "Sign in"}</button>
      </form>
    </div>
  );
}
