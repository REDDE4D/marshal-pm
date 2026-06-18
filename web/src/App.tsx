import { useEffect, useState } from "react";
import { getSession } from "./api";
import { Login } from "./Login";
import { Fleet } from "./Fleet";
import { ProcessDetail } from "./ProcessDetail";
import { useRoute } from "./router";

export function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);
  const route = useRoute();
  useEffect(() => { getSession().then((u) => setAuthed(u !== null)); }, []);
  if (authed === null) return <div className="loading">loading…</div>;
  if (!authed) return <Login onLogin={() => setAuthed(true)} />;
  const onLogout = () => setAuthed(false);
  if (route.name === "detail") return <ProcessDetail agent={route.agent} proc={route.proc} onLogout={onLogout} />;
  return <Fleet onLogout={onLogout} />;
}
