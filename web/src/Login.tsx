import { useRef, useState } from "react";
import { login } from "./api";
import { Field, Button } from "./components/Controls";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [user] = useState("admin");
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const passRef = useRef<HTMLInputElement>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    if (await login(user, pass)) {
      onLogin();
    } else {
      setBusy(false);
      setError("invalid username or password");
      passRef.current?.focus();
    }
  }

  return (
    <div className="loginwrap">
      <form className="loginbox" onSubmit={submit}>
        <span className="wm">mar<b>$</b>hal</span>
        <div className="tagline">self-hosted process fleet</div>
        <Field label="password">
          <input
            ref={passRef}
            className="inp"
            type="password"
            value={pass}
            onChange={(e) => setPass(e.target.value)}
            placeholder="••••••••"
            autoFocus
          />
        </Field>
        {error && <p className="error">{error}</p>}
        <Button type="submit" disabled={busy} style={{ width: "100%", padding: "9px", justifyContent: "center", marginTop: "6px" }}>
          {busy ? "signing in…" : "sign in"}
        </Button>
      </form>
    </div>
  );
}
