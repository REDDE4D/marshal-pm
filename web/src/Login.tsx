import { useState } from "react";
import { login } from "./api";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [user, setUser] = useState("admin");
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (await login(user, pass)) {
      onLogin();
    } else {
      setError("Invalid username or password.");
    }
  }

  return (
    <div className="login">
      <form onSubmit={submit}>
        <h1>Marshal</h1>
        <label>
          Username
          <input value={user} onChange={(e) => setUser(e.target.value)} autoFocus />
        </label>
        <label>
          Password
          <input type="password" value={pass} onChange={(e) => setPass(e.target.value)} />
        </label>
        {error && <p className="error">{error}</p>}
        <button type="submit">Sign in</button>
      </form>
    </div>
  );
}
