import { useState } from "react";
import { login } from "./api";
import { Field, Input, Button } from "./components/Controls";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [user] = useState("admin");
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (await login(user, pass)) onLogin();
    else setError("invalid username or password");
  }

  return (
    <div className="loginwrap">
      <form className="loginbox" onSubmit={submit}>
        <span className="wm">mar<b>$</b>hal</span>
        <div className="tagline">self-hosted process fleet</div>
        <Field label="password">
          <Input
            type="password"
            value={pass}
            onChange={(e) => setPass(e.target.value)}
            placeholder="••••••••"
            autoFocus
          />
        </Field>
        {error && <p className="error">{error}</p>}
        <Button type="submit" style={{ width: "100%", padding: "9px", justifyContent: "center", marginTop: "6px" }}>
          sign in
        </Button>
      </form>
    </div>
  );
}
