import { useEffect, useState } from "react";
import { getSession } from "./api";
import { Login } from "./Login";
import { Fleet } from "./Fleet";

export function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);

  useEffect(() => {
    getSession().then((u) => setAuthed(u !== null));
  }, []);

  if (authed === null) return <div className="loading">Loading…</div>;
  if (!authed) return <Login onLogin={() => setAuthed(true)} />;
  return <Fleet onLogout={() => setAuthed(false)} />;
}
