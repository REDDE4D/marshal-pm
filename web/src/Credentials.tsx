import { useState, useEffect, type FormEvent } from "react";
import { CredentialMeta, listCredentials, createCredential, createSSHCredential, deleteCredential } from "./api";
import { SectionHeader, LedgerHeader, LedgerRow } from "./components/Ledger";
import { Segment, Field, Input, Button } from "./components/Controls";
import { formatDateShort } from "./lib/format";
import { useStatus, StatusMessage } from "./components/StatusMessage";

const COLS = "1.4fr 1fr 1.2fr 0.6fr";

export function Credentials({ onLogout: _onLogout }: { onLogout: () => void }) {
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
  const { status, show } = useStatus();

  async function refresh() {
    const list = await listCredentials();
    setCreds(list);
    setLoading(false);
  }

  useEffect(() => { refresh(); }, []);

  const canSubmit = kind === "ssh-key"
    ? name.trim() !== "" && !busy
    : name.trim() !== "" && username.trim() !== "" && token !== "" && !busy;

  const disabledReason: string | undefined = busy
    ? "saving…"
    : !name.trim()
    ? "enter a name"
    : kind === "https-token" && !username.trim()
    ? "enter a username"
    : kind === "https-token" && !token
    ? "enter a token"
    : undefined;

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
        show("success", "key generated");
      } else {
        const msg = res.error || "error";
        setError(msg);
        show("error", msg);
      }
    } else {
      const res = await createCredential(name.trim(), username.trim(), token);
      setBusy(false);
      if (res.ok) {
        setName("");
        setUsername("");
        setToken("");
        await refresh();
        show("success", "credential added");
      } else {
        const msg = res.error || "error";
        setError(msg);
        show("error", msg);
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
    <>
      {/* ── 01 Stored ── */}
      <SectionHeader index="01" title="Stored" count={loading ? undefined : `${creds.length} credentials`} />

      {loading ? (
        <p className="sub" style={{ padding: "12px 22px" }}>loading…</p>
      ) : creds.length === 0 ? (
        <p className="sub" style={{ padding: "12px 22px" }}>no credentials stored.</p>
      ) : (
        <>
          <LedgerHeader cols={COLS}>
            <div>Name</div>
            <div>Type</div>
            <div>Added</div>
            <div className="rr" />
          </LedgerHeader>

          {creds.map((c) => (
            <div key={c.name}>
              <LedgerRow cols={COLS}>
                <div className="nm">{c.name}</div>
                <div className={`v ${c.type === "ssh-key" ? "indigo" : "sky"}`}>
                  {c.type === "ssh-key" ? "ssh-key" : `https · ${c.username}`}
                </div>
                <div className="v" style={{ fontSize: "11px", color: "var(--dim)" }}>
                  {formatDateShort(c.created_at)}
                </div>
                <div className="rr">
                  {deleteTarget === c.name ? (
                    <div style={{ display: "flex", gap: "6px" }}>
                      <Button variant="dgr" size="sm" onClick={() => handleDelete(c.name)}>
                        confirm
                      </Button>
                      <Button size="sm" onClick={() => setDeleteTarget(null)}>
                        cancel
                      </Button>
                    </div>
                  ) : (
                    <Button variant="dgr" size="sm" onClick={() => setDeleteTarget(c.name)}>
                      delete
                    </Button>
                  )}
                </div>
              </LedgerRow>

              {/* SSH public-key display on stored credentials */}
              {c.type === "ssh-key" && c.public_key && (
                <div style={{ padding: "8px 20px 12px", borderBottom: "1px solid var(--line2)" }}>
                  <div className="sub" style={{ marginBottom: "6px" }}>public key (deploy key)</div>
                  <pre style={{ margin: "0 0 6px", fontSize: "11px", color: "var(--dim)", whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
                    {c.public_key}
                  </pre>
                  <Button size="sm" onClick={() => navigator.clipboard.writeText(c.public_key!)}>
                    copy
                  </Button>
                </div>
              )}
            </div>
          ))}
        </>
      )}

      {/* ── 02 Add credential ── */}
      <SectionHeader index="02" title="Add credential" />

      <form onSubmit={handleCreate}>
        <div style={{ padding: "8px 20px 0", maxWidth: "560px" }}>
          <Field
            label="type"
            hint={kind === "ssh-key"
              ? "generates an ed25519 key pair — add the public key as a deploy key on your repo"
              : "stores your personal access token for HTTPS git operations"}
          >
            <Segment<"https-token" | "ssh-key">
              options={[
                { value: "https-token", label: "https token" },
                { value: "ssh-key", label: "ssh key" },
              ]}
              value={kind}
              onChange={(v) => { setKind(v); setNewPublicKey(""); setError(""); }}
            />
          </Field>

          <Field label="name">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-github"
            />
          </Field>

          {kind === "https-token" && (
            <>
              <Field label="username">
                <Input
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="github-user"
                />
              </Field>
              <Field label="token">
                <Input
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder="ghp_…"
                  autoComplete="new-password"
                />
              </Field>
            </>
          )}

          {error && (
            <p className="sub" style={{ color: "var(--rose)", margin: "8px 0" }}>{error}</p>
          )}

          {newPublicKey && (
            <div style={{ margin: "12px 0" }}>
              <div className="sub" style={{ marginBottom: "6px" }}>
                generated public key — add this as a deploy key on your repo
                (e.g. GitHub → Settings → Deploy keys → Add deploy key).
              </div>
              <textarea
                readOnly
                rows={3}
                value={newPublicKey}
                style={{ width: "100%", resize: "vertical", boxSizing: "border-box" }}
              />
              <Button
                type="button"
                size="sm"
                style={{ marginTop: "6px" }}
                onClick={() => navigator.clipboard.writeText(newPublicKey)}
              >
                copy
              </Button>
            </div>
          )}
        </div>

        <div className="actions">
          <Button type="submit" disabledReason={disabledReason}>
            {busy ? "saving…" : kind === "ssh-key" ? "generate key" : "add credential"}
          </Button>
          <StatusMessage status={status} />
        </div>
      </form>
    </>
  );
}
