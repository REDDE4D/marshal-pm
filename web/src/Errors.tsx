import { useEffect, useState } from "react";
import { getErrors, type ErrorsResponse } from "./api";
import { Sparkline } from "./Sparkline";

const RANGES = ["24h", "7d", "all"];

function ago(unixSec: number): string {
  if (!unixSec) return "—";
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unixSec));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function Errors() {
  const [range, setRange] = useState("24h");
  const [data, setData] = useState<ErrorsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setErr(null);
    setData(null);
    getErrors(range)
      .then((d) => live && setData(d))
      .catch((e) => live && setErr(String(e)));
    return () => {
      live = false;
    };
  }, [range]);

  return (
    <div className="app">
      <header className="topbar">
        <h1 style={{ margin: 0, fontSize: "18px", fontWeight: 700, color: "#F2F3F7" }}>Errors</h1>
        <button className="btn" onClick={() => { window.location.hash = "#/"; }}>← fleet</button>
      </header>

      <div className="range-tabs">
        {RANGES.map((r) => (
          <button key={r} className={r === range ? "btn btn-active" : "btn"} onClick={() => setRange(r)}>{r}</button>
        ))}
      </div>

      {err && <p className="error">Failed to load: {err}</p>}
      {!err && !data && <p className="empty">Loading…</p>}

      {data && (
        <>
          <div className="cluster-bar">
            <span>Errors <b>{data.cluster.errors}</b></span>
            <span>Signatures <b>{data.cluster.signatures}</b></span>
            <span>Affected procs <b>{data.cluster.affected_procs}</b></span>
            <span>Last error <b>{ago(data.cluster.last_error_unix)}</b></span>
          </div>
          {data.truncated && <p className="warn">Showing a partial window (scan cap reached).</p>}
          {data.signatures.length === 0 ? (
            <p className="empty">No errors in this window.</p>
          ) : (
            <table className="err-ledger">
              <thead>
                <tr>
                  <th>Message</th>
                  <th>Source</th>
                  <th>Where</th>
                  <th>Count</th>
                  <th>Last</th>
                  <th>Trend</th>
                </tr>
              </thead>
              <tbody>
                {data.signatures.map((s) => (
                  <tr key={s.id}>
                    <td className="err-sample">{s.sample}</td>
                    <td className="err-mono">{s.source || "—"}</td>
                    <td>{s.agent} · {s.affected.length} proc{s.affected.length === 1 ? "" : "s"}</td>
                    <td>{s.count}</td>
                    <td>{ago(s.last_unix)}</td>
                    <td><Sparkline points={s.buckets} color="#E5707E" /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </div>
  );
}
