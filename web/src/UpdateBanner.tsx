import { useState } from "react";
import { UpdateStatus } from "./api";

const RELEASES_URL = "https://github.com/REDDE4D/marshal-pm/releases";

// dismissKey scopes the dismissal to the specific latest version, so a banner
// the user dismissed reappears only when a newer release than that shows up.
function dismissKey(latest: string): string {
  return `marshal.update.dismissed.${latest}`;
}

export function UpdateBanner({ update }: { update: UpdateStatus }) {
  const agents = update.outdated_agents?.length ?? 0;
  const relevant = update.enabled && (update.outdated || agents > 0);
  const [dismissed, setDismissed] = useState(
    () => relevant && localStorage.getItem(dismissKey(update.latest)) === "1"
  );

  if (!relevant || dismissed) return null;

  function dismiss() {
    try {
      localStorage.setItem(dismissKey(update.latest), "1");
    } catch {
      /* private mode / storage disabled — dismiss for this session only */
    }
    setDismissed(true);
  }

  return (
    <div className="updbar" role="status">
      <span className="updbar-dot" aria-hidden="true">↑</span>
      <span className="updbar-txt">
        {update.outdated ? (
          <>
            Marshal <b>{update.latest}</b> is available
            {update.current ? <> — this server runs {update.current}</> : null}
          </>
        ) : (
          <>Marshal {update.latest} is the latest release</>
        )}
        {agents > 0 && (
          <>
            {" · "}
            {agents} agent{agents === 1 ? "" : "s"} on an older version
          </>
        )}
      </span>
      <a className="updbar-lnk" href={RELEASES_URL} target="_blank" rel="noreferrer">
        release notes ↗
      </a>
      <button className="updbar-x" aria-label="Dismiss update notice" onClick={dismiss}>
        ✕
      </button>
    </div>
  );
}
