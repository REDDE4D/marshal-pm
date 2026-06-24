import { ReactNode, useEffect, useState } from "react";
import { useRoute, navigate, logsHref } from "./router";
import { navItemFor } from "./lib/nav";
import { getErrors } from "./api";
import { ErrorBoundary } from "./ErrorBoundary";

interface AppShellProps {
  ctx: ReactNode;
  right?: ReactNode;
  onLogout: () => void;
  children: ReactNode;
}

export function AppShell({ ctx, right, onLogout, children }: AppShellProps) {
  const route = useRoute();
  const active = navItemFor(route);
  const [badge, setBadge] = useState(0);

  useEffect(() => {
    let cancelled = false;

    async function poll() {
      try {
        const r = await getErrors("24h");
        if (!cancelled) setBadge(r.cluster.signatures);
      } catch {
        // swallow — never throw, never logout
      }
    }

    poll();
    const id = setInterval(poll, 15_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  return (
    <div className="shell">
      <nav className="rail">
        <div className="mk">m$</div>

        <button
          className={`ri${active === "fleet" ? " on" : ""}`}
          aria-label="Fleet"
          onClick={() => navigate("#/")}
        >
          <span className="ic">▦</span>
          <span className="lb">Fleet</span>
        </button>

        <button
          className={`ri${active === "errors" ? " on" : ""}`}
          aria-label="Errors"
          onClick={() => navigate("#/errors")}
        >
          {badge > 0 && <span className="badge">{badge}</span>}
          <span className="ic">⚠</span>
          <span className="lb">Errors</span>
        </button>

        <button
          className={`ri${active === "logs" ? " on" : ""}`}
          aria-label="Logs"
          onClick={() => navigate(logsHref())}
        >
          <span className="ic">▤</span>
          <span className="lb">Logs</span>
        </button>

        <button
          className={`ri${active === "notif" ? " on" : ""}`}
          aria-label="Notify"
          onClick={() => navigate("#/notifications")}
        >
          <span className="ic">◔</span>
          <span className="lb">Notify</span>
        </button>

        <button
          className={`ri${active === "creds" ? " on" : ""}`}
          aria-label="Credentials"
          onClick={() => navigate("#/credentials")}
        >
          <span className="ic">⚿</span>
          <span className="lb">Creds</span>
        </button>

        <div className="sp" />
        {/* Settings: no global-settings page yet; omitted per hardening */}
      </nav>

      <div className="main">
        <div className="top">
          <span className="ctx">{ctx}</span>
          <div className="rt">
            {right}
            <button className="lnk" onClick={onLogout}>sign out</button>
          </div>
        </div>

        <ErrorBoundary>
          <div className="content">{children}</div>
        </ErrorBoundary>
      </div>
    </div>
  );
}
