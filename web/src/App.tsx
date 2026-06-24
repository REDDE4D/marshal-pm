import { useEffect, useState } from "react";
import { getSession } from "./api";
import { Login } from "./Login";
import { Overview } from "./Overview";
import { ProcessDetail } from "./ProcessDetail";
import { Credentials } from "./Credentials";
import { Notifications } from "./Notifications";
import { Errors } from "./Errors";
import { useRoute } from "./router";

export function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);
  const route = useRoute();
  useEffect(() => { getSession().then((u) => setAuthed(u !== null)); }, []);
  if (authed === null) return <div className="loading">loading…</div>;
  if (!authed) return <Login onLogin={() => setAuthed(true)} />;
  const onLogout = () => setAuthed(false);
  if (route.name === "detail") return <ProcessDetail agent={route.agent} proc={route.proc} onLogout={onLogout} />;
  if (route.name === "credentials") return <Credentials onLogout={onLogout} />;
  if (route.name === "notifications") return <Notifications />;
  if (route.name === "errors") return <Errors />;
  return <Overview onLogout={onLogout} />;
}
