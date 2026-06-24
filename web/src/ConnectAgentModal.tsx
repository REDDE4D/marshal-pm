import { useState } from "react";
import { connectToken } from "./api";
import { Modal } from "./components/Modal";
import { Field, Input, Button } from "./components/Controls";

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

  const footer = (
    <>
      {cmd ? (
        <Button
          type="button"
          onClick={async () => {
            await navigator.clipboard.writeText(cmd);
            setCopied(true);
          }}
        >
          {copied ? "copied" : "copy command"}
        </Button>
      ) : (
        <Button variant="ghost" type="button" onClick={onClose}>close</Button>
      )}
    </>
  );

  return (
    <Modal title="Connect agent" onClose={onClose} footer={footer}>
      <Field label="agent name">
        <Input value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="server address">
        <Input
          value={address}
          onChange={(e) => setAddress(e.target.value)}
          placeholder="auto (host:fleet-port)"
        />
      </Field>
      <Button type="button" disabled={busy} onClick={generate}>
        generate connect command
      </Button>
      {error && <p className="error">{error}</p>}
      {cmd && (
        <>
          <p className="warn" style={{ marginTop: "12px" }}>
            Shown once. Generating rotated the enroll token — any previously generated, unused command no longer works. Already-connected agents are unaffected.
          </p>
          <pre className="connect-cmd">{cmd}</pre>
        </>
      )}
    </Modal>
  );
}
