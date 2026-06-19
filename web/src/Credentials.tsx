import { useState, useEffect, type FormEvent } from "react";
import { CredentialMeta, listCredentials, createCredential, deleteCredential } from "./api";
import { Logo } from "./Logo";

function fmtDate(unixSec: number): string {
  if (!unixSec) return "—";
  return new Date(unixSec * 1000).toLocaleString();
}

export function Credentials({ onLogout }: { onLogout: () => void }) {
  const [creds, setCreds] = useState<CredentialMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);

  async function refresh() {
    const list = await listCredentials();
    setCreds(list);
    setLoading(false);
  }

  useEffect(() => { refresh(); }, []);

  const canSubmit = name.trim() !== "" && username.trim() !== "" && token !== "" && !busy;

  async function handleCreate(e: FormEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError("");
    const res = await createCredential(name.trim(), username.trim(), token);
    setBusy(false);
    if (res.ok) {
      setName("");
      setUsername("");
      setToken("");
      await refresh();
    } else {
      setError(res.error || "error");
    }
  }

  async function handleDelete(credName: string) {
    if (deleteTarget !== credName) {
      setDeleteTarget(credName);
      return;
    }
    setDeleteTarget(null);
    const res = await deleteCredential(credName);
    if (!res.ok) {
      setError(res.error || "error");
    } else {
      await refresh();
    }
  }

  return (
    <div className="app">
      <div className="topbar">
        <Logo />
        <div className="topbar-actions">
          <button className="btn" onClick={() => { window.location.hash = ""; }}>← fleet</button>
          <button className="btn" onClick={onLogout}>sign out</button>
        </div>
      </div>

      <div className="cred-section">
        <div className="cred-head">git credentials</div>

        {loading ? (
          <p className="loading">loading…</p>
        ) : creds.length === 0 ? (
          <p className="empty">no credentials stored.</p>
        ) : (
          <div className="cred-list">
            {creds.map((c) => (
              <div className="cred-row card" key={c.name}>
                <div className="cred-row-info">
                  <span className="cred-name">{c.name}</span>
                  <span className="cred-meta">{c.username} · {c.type} · added {fmtDate(c.created_at)}</span>
                </div>
                <div className="ctl">
                  {deleteTarget === c.name ? (
                    <>
                      <button
                        className="ctl-btn danger"
                        onClick={() => handleDelete(c.name)}
                      >
                        confirm delete
                      </button>
                      <button className="ctl-btn" onClick={() => setDeleteTarget(null)}>cancel</button>
                    </>
                  ) : (
                    <button className="ctl-btn danger" onClick={() => handleDelete(c.name)}>delete</button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}

        <form className="cred-form" onSubmit={handleCreate}>
          <div className="cred-form-title">add credential</div>
          <label className="field">
            name
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="my-github" />
          </label>
          <label className="field">
            username
            <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="github-user" />
          </label>
          <label className="field">
            token
            <input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="ghp_…"
              autoComplete="new-password"
            />
          </label>
          {error && <div className="modal-error">{error}</div>}
          <div className="cred-form-foot">
            <button type="submit" className="btn primary" disabled={!canSubmit}>
              {busy ? "saving…" : "add credential"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
