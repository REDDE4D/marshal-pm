import { useEffect, useState } from "react";
import { getSession } from "./api";
import { Login } from "./Login";
import { Overview } from "./Overview";
import { ProcessDetail } from "./ProcessDetail";
import { Credentials } from "./Credentials";
import { Notifications } from "./Notifications";
import { Errors } from "./Errors";
import { AppShell } from "./AppShell";
import { useRoute } from "./router";

export function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);
  const route = useRoute();
  useEffect(() => { getSession().then((u) => setAuthed(u !== null)); }, []);
  if (authed === null) return <div className="loading">loading…</div>;
  if (!authed) return <Login onLogin={() => setAuthed(true)} />;
  const onLogout = () => setAuthed(false);

  if (route.name === "detail") {
    return (
      <AppShell ctx="Fleet" onLogout={onLogout}>
        <ProcessDetail agent={route.agent} proc={route.proc} onLogout={onLogout} />
      </AppShell>
    );
  }

  if (route.name === "credentials") {
    return (
      <AppShell ctx="Credentials" onLogout={onLogout}>
        <Credentials onLogout={onLogout} />
      </AppShell>
    );
  }

  if (route.name === "notifications") {
    return (
      <AppShell ctx="Notifications" onLogout={onLogout}>
        <Notifications />
      </AppShell>
    );
  }

  if (route.name === "errors") {
    return (
      <AppShell ctx="Errors" onLogout={onLogout}>
        <Errors />
      </AppShell>
    );
  }

  if (route.name === "logs") {
    // TODO(Task12): real Logs page; route to Overview until then
    return (
      <AppShell ctx="Fleet" onLogout={onLogout}>
        <Overview onLogout={onLogout} />
      </AppShell>
    );
  }

  // overview (default)
  return (
    <AppShell ctx="Fleet" onLogout={onLogout}>
      <Overview onLogout={onLogout} />
    </AppShell>
  );
}
