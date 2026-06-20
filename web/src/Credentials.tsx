import { useState, useEffect, type FormEvent } from "react";
import { CredentialMeta, listCredentials, createCredential, createSSHCredential, deleteCredential } from "./api";
import { Logo } from "./Logo";

function fmtDate(unixSec: number): string {
  if (!unixSec) return "—";
  return new Date(unixSec * 1000).toLocaleString();
}

export function Credentials({ onLogout }: { onLogout: () => void }) {
  const [creds, setCreds] = useState<CredentialMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [kind, setKind] = useState<"https-token" | "ssh-key">("https-token");
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [newPublicKey, setNewPublicKey] = useState("");

  async function refresh() {
    const list = await listCredentials();
    setCreds(list);
    setLoading(false);
  }

  useEffect(() => { refresh(); }, []);

  const canSubmit = kind === "ssh-key"
    ? name.trim() !== "" && !busy
    : name.trim() !== "" && username.trim() !== "" && token !== "" && !busy;

  async function handleCreate(e: FormEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError("");
    setNewPublicKey("");

    if (kind === "ssh-key") {
      const res = await createSSHCredential(name.trim());
      setBusy(false);
      if (res.ok) {
        setNewPublicKey(res.public_key ?? "");
        setName("");
        await refresh();
      } else {
        setError(res.error || "error");
      }
    } else {
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
                  <span className="cred-meta">
                    {c.type === "ssh-key" ? "ssh-key" : `${c.username} · https-token`}
                    {" · added "}{fmtDate(c.created_at)}
                  </span>
                  {c.type === "ssh-key" && c.public_key && (
                    <div className="cred-pubkey">
                      <div className="cred-pubkey-label">public key (deploy key)</div>
                      <pre className="cred-pubkey-text">{c.public_key}</pre>
                      <button
                        className="btn"
                        onClick={() => navigator.clipboard.writeText(c.public_key!)}
                      >
                        copy
                      </button>
                    </div>
                  )}
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
          <div className="field">
            <label className="field-label">type</label>
            <div className="cred-type-toggle">
              <label>
                <input
                  type="radio"
                  name="cred-kind"
                  value="https-token"
                  checked={kind === "https-token"}
                  onChange={() => { setKind("https-token"); setNewPublicKey(""); setError(""); }}
                />
                {" "}HTTPS token
              </label>
              <label>
                <input
                  type="radio"
                  name="cred-kind"
                  value="ssh-key"
                  checked={kind === "ssh-key"}
                  onChange={() => { setKind("ssh-key"); setNewPublicKey(""); setError(""); }}
                />
                {" "}SSH key
              </label>
            </div>
          </div>
          <label className="field">
            name
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="my-github" />
          </label>
          {kind === "https-token" && (
            <>
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
            </>
          )}
          {error && <div className="modal-error">{error}</div>}
          {newPublicKey && (
            <div className="cred-pubkey-new">
              <div className="cred-pubkey-label">
                generated public key — add this as a deploy key on your repo
                (e.g. GitHub → Settings → Deploy keys → Add deploy key).
              </div>
              <textarea
                className="cred-pubkey-text"
                readOnly
                rows={3}
                value={newPublicKey}
              />
              <button
                type="button"
                className="btn"
                onClick={() => navigator.clipboard.writeText(newPublicKey)}
              >
                copy
              </button>
            </div>
          )}
          <div className="cred-form-foot">
            <button type="submit" className="btn primary" disabled={!canSubmit}>
              {busy ? "saving…" : kind === "ssh-key" ? "generate key" : "add credential"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
