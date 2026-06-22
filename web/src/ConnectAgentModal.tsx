import { useState } from "react";
import { connectToken } from "./api";

export function ConnectAgentModal({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("agent");
  const [address, setAddress] = useState("");
  const [cmd, setCmd] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);

  async function generate() {
    setBusy(true);
    setError("");
    setCopied(false);
    try {
      const info = await connectToken(address, name);
      const addr = address.trim() || info.default_address;
      setAddress(addr);
      setCmd(
        `cat > marshal.yaml <<'EOF'\n` +
          `server:\n` +
          `  address: ${addr}\n` +
          `  name: ${name.trim() || "agent"}\n` +
          `  token: ${info.token}\n` +
          `  fingerprint: ${info.fingerprint}\n` +
          `EOF\n` +
          `marshal start marshal.yaml`,
      );
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>Connect an agent</h3>
        <label>agent name<input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <label>server address<input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="auto (host:fleet-port)" /></label>
        <button className="btn" disabled={busy} onClick={generate}>Generate connect command</button>
        {error && <p className="error">{error}</p>}
        {cmd && (
          <>
            <p className="warn">Shown once. Generating rotated the enroll token — any previously generated, unused command no longer works. Already-connected agents are unaffected.</p>
            <pre className="connect-cmd">{cmd}</pre>
            <button className="btn" onClick={async () => { await navigator.clipboard.writeText(cmd); setCopied(true); }}>{copied ? "copied" : "copy"}</button>
          </>
        )}
        <button className="btn" onClick={onClose}>close</button>
      </div>
    </div>
  );
}
