import { useEffect, useState } from "react";
import {
  getNotifications, putChannel, deleteChannel, testChannel,
  putRule, deleteRule, putNotifSettings,
  type NotifConfig, type NotifChannel, type NotifRule,
} from "./api";

const EVENT_TYPES = ["crash", "restart_loop", "agent_down", "agent_up", "deploy_fail", "recovered"];
const CHANNEL_TYPES = ["webhook", "telegram", "slack", "email"];

// config + secret field names per channel type (secret fields are write-only)
const CONFIG_FIELDS: Record<string, string[]> = {
  webhook: ["url"],
  telegram: ["chat_id"],
  slack: [],
  email: ["host", "port", "from", "to", "username", "tls"],
};
const SECRET_FIELDS: Record<string, string[]> = {
  webhook: ["hmac"],
  telegram: ["bot_token"],
  slack: ["webhook_url"],
  email: ["password"],
};

export function Notifications() {
  const [cfg, setCfg] = useState<NotifConfig | null>(null);
  const [err, setErr] = useState("");

  async function reload() {
    try {
      setCfg(await getNotifications());
    } catch {
      setErr("failed to load");
    }
  }
  useEffect(() => {
    reload();
  }, []);

  if (!cfg) return <div className="panel">Loading… {err}</div>;

  return (
    <div className="panel">
      <h2>Notifications</h2>
      {err && <div className="error">{err}</div>}
      <ChannelSection cfg={cfg} onChange={reload} />
      <RuleSection cfg={cfg} onChange={reload} />
      <SettingsSection cfg={cfg} onChange={reload} />
    </div>
  );
}

function ChannelSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [type, setType] = useState("webhook");
  const [name, setName] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState("");

  async function submit() {
    const config: Record<string, string> = {};
    const secrets: Record<string, string> = {};
    for (const f of CONFIG_FIELDS[type]) config[f] = fields[f] ?? "";
    for (const f of SECRET_FIELDS[type]) if (fields[f]) secrets[f] = fields[f];
    const res = await putChannel({ name, type, enabled, config, secrets });
    setMsg(res.ok ? "saved" : res.error ?? "error");
    if (res.ok) {
      setName("");
      setFields({});
      onChange();
    }
  }

  return (
    <section>
      <h3>Channels</h3>
      <ul>
        {cfg.channels.map((c: NotifChannel) => (
          <li key={c.name}>
            <strong>{c.name}</strong> ({c.type}) {c.enabled ? "on" : "off"}{" "}
            {c.has_secret ? "🔒" : ""}
            <button onClick={async () => { const r = await testChannel(c.name); setMsg(r.ok ? "test sent" : r.error ?? "test failed"); }}>Test</button>
            <button onClick={async () => { await deleteChannel(c.name); onChange(); }}>Delete</button>
          </li>
        ))}
      </ul>
      <div>
        <select value={type} onChange={(e) => { setType(e.target.value); setFields({}); }}>
          {CHANNEL_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
        <label><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> enabled</label>
        {[...CONFIG_FIELDS[type], ...SECRET_FIELDS[type]].map((f) => (
          <input
            key={f}
            placeholder={f}
            type={SECRET_FIELDS[type].includes(f) ? "password" : "text"}
            value={fields[f] ?? ""}
            onChange={(e) => setFields({ ...fields, [f]: e.target.value })}
          />
        ))}
        <button onClick={submit}>Save channel</button>
        <span>{msg}</span>
      </div>
    </section>
  );
}

function RuleSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [name, setName] = useState("");
  const [events, setEvents] = useState<string[]>([]);
  const [agent, setAgent] = useState("*");
  const [process, setProcess] = useState("*");
  const [chans, setChans] = useState<string[]>([]);
  const [msg, setMsg] = useState("");

  function toggle(list: string[], v: string, set: (x: string[]) => void) {
    set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  }

  async function submit() {
    const rule: NotifRule = { name, enabled: true, events, agent, process, channels: chans };
    const res = await putRule(rule);
    setMsg(res.ok ? "saved" : res.error ?? "error");
    if (res.ok) { setName(""); setEvents([]); setChans([]); onChange(); }
  }

  return (
    <section>
      <h3>Rules</h3>
      <ul>
        {cfg.rules.map((r: NotifRule) => (
          <li key={r.name}>
            <strong>{r.name}</strong>: {r.events.length ? r.events.join(",") : "any"} ·
            {r.agent || "*"}/{r.process || "*"} → {r.channels.join(",")}
            <button onClick={async () => { await deleteRule(r.name); onChange(); }}>Delete</button>
          </li>
        ))}
      </ul>
      <div>
        <input placeholder="rule name" value={name} onChange={(e) => setName(e.target.value)} />
        <div>{EVENT_TYPES.map((ev) => (
          <label key={ev}><input type="checkbox" checked={events.includes(ev)} onChange={() => toggle(events, ev, setEvents)} /> {ev}</label>
        ))}</div>
        <input placeholder="agent (* = any)" value={agent} onChange={(e) => setAgent(e.target.value)} />
        <input placeholder="process (* = any)" value={process} onChange={(e) => setProcess(e.target.value)} />
        <div>{cfg.channels.map((c) => (
          <label key={c.name}><input type="checkbox" checked={chans.includes(c.name)} onChange={() => toggle(chans, c.name, setChans)} /> {c.name}</label>
        ))}</div>
        <button onClick={submit}>Save rule</button>
        <span>{msg}</span>
      </div>
    </section>
  );
}

function SettingsSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [cooldown, setCooldown] = useState(cfg.settings.cooldown_seconds);
  const [recovery, setRecovery] = useState(!cfg.settings.suppress_recovery);
  const [overrides, setOverrides] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {};
    for (const ev of EVENT_TYPES) {
      const v = cfg.settings.cooldown_overrides?.[ev];
      init[ev] = v === undefined ? "" : String(v);
    }
    return init;
  });
  return (
    <section>
      <h3>Settings</h3>
      <label>Cooldown (seconds): <input type="number" value={cooldown} onChange={(e) => setCooldown(Number(e.target.value))} /></label>
      <label><input type="checkbox" checked={recovery} onChange={(e) => setRecovery(e.target.checked)} /> Send recovery notices</label>
      <div>
        <h4>Per-event cooldown (seconds)</h4>
        {EVENT_TYPES.map((ev) => (
          <label key={ev}>{ev}: <input type="number" placeholder={`${cooldown} (global)`} value={overrides[ev]} onChange={(e) => setOverrides({ ...overrides, [ev]: e.target.value })} /></label>
        ))}
      </div>
      <button onClick={async () => {
        const co: Record<string, number> = {};
        for (const ev of EVENT_TYPES) {
          if (overrides[ev] !== "") co[ev] = Number(overrides[ev]);
        }
        await putNotifSettings({ cooldown_seconds: cooldown, suppress_recovery: !recovery, cooldown_overrides: co });
        onChange();
      }}>Save</button>
    </section>
  );
}
