import { useState } from "react";
import { login } from "./api";
import { Logo } from "./Logo";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [user, setUser] = useState("admin");
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (await login(user, pass)) onLogin();
    else setError("invalid username or password");
  }

  return (
    <div className="login">
      <form onSubmit={submit}>
        <Logo />
        <label>username<input value={user} onChange={(e) => setUser(e.target.value)} autoFocus /></label>
        <label>password<input type="password" value={pass} onChange={(e) => setPass(e.target.value)} /></label>
        {error && <p className="error">{error}</p>}
        <button type="submit">sign in</button>
      </form>
    </div>
  );
}
