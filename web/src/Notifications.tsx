import { useEffect, useState } from "react";
import {
  getNotifications, putChannel, deleteChannel, testChannel,
  putRule, deleteRule, putNotifSettings,
  type NotifConfig, type NotifChannel, type NotifRule,
} from "./api";
import { SectionHeader, LedgerHeader, LedgerRow } from "./components/Ledger";
import { Toggle, Chip, Field, Input, Button } from "./components/Controls";

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

// Color classes for channel types
const CHANNEL_COLOR: Record<string, string> = {
  webhook: "indigo",
  telegram: "teal",
  slack: "sky",
  email: "amber",
};

// Color classes for event types
const EVENT_COLOR: Record<string, string> = {
  crash: "rose",
  restart_loop: "amber",
  agent_down: "sky",
  agent_up: "teal",
  deploy_fail: "indigo",
  recovered: "olive",
};

const CHANNEL_COLS = "1.4fr 0.7fr 0.6fr 0.6fr 1fr";
const RULE_COLS = "1fr 1.5fr 0.9fr 1fr 0.5fr";

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

  if (!cfg) {
    return (
      <p className="sub" style={{ padding: "12px 22px" }}>
        {err ? `Error: ${err}` : "Loading…"}
      </p>
    );
  }

  return (
    <>
      {err && (
        <p className="sub" style={{ padding: "12px 22px", color: "var(--rose)" }}>
          {err}
        </p>
      )}
      <ChannelSection cfg={cfg} onChange={reload} />
      <RuleSection cfg={cfg} onChange={reload} />
      <SettingsSection cfg={cfg} onChange={reload} />
    </>
  );
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

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

  const allFields = [...CONFIG_FIELDS[type], ...SECRET_FIELDS[type]];

  return (
    <>
      <SectionHeader
        index="01"
        title="Channels"
        count={`${cfg.channels.length} configured`}
      />

      {cfg.channels.length > 0 && (
        <>
          <LedgerHeader cols={CHANNEL_COLS}>
            <div>Name</div>
            <div>Type</div>
            <div>State</div>
            <div>Secret</div>
            <div className="rr">Actions</div>
          </LedgerHeader>

          {cfg.channels.map((c: NotifChannel) => (
            <LedgerRow
              key={c.name}
              cols={CHANNEL_COLS}
              actions={[
                {
                  icon: "◎",
                  label: "test",
                  variant: undefined,
                  onClick: async () => {
                    const r = await testChannel(c.name);
                    setMsg(r.ok ? "test sent" : r.error ?? "test failed");
                  },
                },
                {
                  icon: "✕",
                  label: "delete",
                  variant: "dgr",
                  onClick: async () => {
                    await deleteChannel(c.name);
                    onChange();
                  },
                },
              ]}
            >
              <div className="nm">{c.name}</div>
              <div className={`v ${CHANNEL_COLOR[c.type] ?? ""}`}>{c.type}</div>
              <div className="st">
                <span className={`sq ${c.enabled ? "on" : "st"}`} />
                <span className={c.enabled ? "on-t" : "stp-t"}>
                  {c.enabled ? "enabled" : "disabled"}
                </span>
              </div>
              <div className={c.has_secret ? "v olive" : "v"}>
                {c.has_secret ? "🔒 set" : "—"}
              </div>
            </LedgerRow>
          ))}
        </>
      )}

      {/* Add channel form */}
      <div className="actions" style={{ display: "block", padding: "16px 20px 4px" }}>
        <div style={{ display: "grid", gridTemplateColumns: "120px 1fr", gap: "14px", maxWidth: "560px" }}>
          <Field label="type">
            <select
              className="inp"
              value={type}
              onChange={(e) => {
                setType(e.target.value);
                setFields({});
              }}
            >
              {CHANNEL_TYPES.map((t) => (
                <option key={t} value={t}>{t}</option>
              ))}
            </select>
          </Field>
          <Field label="name">
            <Input
              placeholder="channel-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: "12px", margin: "8px 0" }}>
          <Toggle
            on={enabled}
            onChange={setEnabled}
            label="Enabled"
          />
        </div>

        {allFields.length > 0 && (
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(180px, 1fr))", gap: "12px", maxWidth: "700px", marginBottom: "12px" }}>
            {allFields.map((f) => (
              <Field key={f} label={f}>
                <Input
                  placeholder={f}
                  type={SECRET_FIELDS[type].includes(f) ? "password" : "text"}
                  value={fields[f] ?? ""}
                  onChange={(e) => setFields({ ...fields, [f]: e.target.value })}
                  autoComplete={SECRET_FIELDS[type].includes(f) ? "new-password" : undefined}
                />
              </Field>
            ))}
          </div>
        )}
      </div>

      <div className="actions">
        <Button onClick={submit} disabled={!name.trim()}>
          + add channel
        </Button>
        {msg && (
          <span className="sub" style={{ marginLeft: "12px", color: msg === "saved" || msg === "test sent" ? "var(--teal)" : "var(--rose)" }}>
            {msg}
          </span>
        )}
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

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
    if (res.ok) {
      setName("");
      setEvents([]);
      setChans([]);
      onChange();
    }
  }

  return (
    <>
      <SectionHeader
        index="02"
        title="Rules"
        count={`${cfg.rules.length} rule${cfg.rules.length !== 1 ? "s" : ""}`}
      />

      {cfg.rules.length > 0 && (
        <>
          <LedgerHeader cols={RULE_COLS}>
            <div>Name</div>
            <div>Events</div>
            <div>Target</div>
            <div>Channels</div>
            <div className="rr" />
          </LedgerHeader>

          {cfg.rules.map((r: NotifRule) => (
            <LedgerRow
              key={r.name}
              cols={RULE_COLS}
              actions={[
                {
                  icon: "✕",
                  label: "delete",
                  variant: "dgr",
                  onClick: async () => {
                    await deleteRule(r.name);
                    onChange();
                  },
                },
              ]}
            >
              <div className="nm">{r.name}</div>
              <div style={{ fontSize: "11px", display: "flex", flexWrap: "wrap", gap: "4px", alignItems: "center" }}>
                {r.events.length === 0 ? (
                  <span style={{ color: "var(--dim)" }}>any</span>
                ) : (
                  r.events.map((ev) => (
                    <span key={ev} className={EVENT_COLOR[ev] ?? ""}>{ev}</span>
                  ))
                )}
              </div>
              <div className="v" style={{ fontSize: "11px", color: "var(--dim)" }}>
                {r.agent || "*"} / {r.process || "*"}
              </div>
              <div className="v teal" style={{ fontSize: "11px" }}>
                {r.channels.join(", ") || "—"}
              </div>
            </LedgerRow>
          ))}
        </>
      )}

      {/* Add rule form */}
      <div style={{ padding: "16px 20px 4px" }}>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "14px", maxWidth: "560px" }}>
          <Field label="rule name">
            <Input
              placeholder="my-rule"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field label="target (agent / process)">
            <div style={{ display: "flex", gap: "8px" }}>
              <Input
                placeholder="agent (*)"
                value={agent}
                onChange={(e) => setAgent(e.target.value)}
                style={{ flex: 1 }}
              />
              <Input
                placeholder="process (*)"
                value={process}
                onChange={(e) => setProcess(e.target.value)}
                style={{ flex: 1 }}
              />
            </div>
          </Field>
        </div>

        <div style={{ marginTop: "12px" }}>
          <label
            style={{
              fontSize: "10px",
              letterSpacing: ".08em",
              textTransform: "uppercase",
              color: "var(--dim)",
              display: "block",
              marginBottom: "8px",
            }}
          >
            events
          </label>
          <div style={{ display: "flex", flexWrap: "wrap", gap: "7px" }}>
            {EVENT_TYPES.map((ev) => (
              <Chip
                key={ev}
                label={ev}
                on={events.includes(ev)}
                onClick={() => toggle(events, ev, setEvents)}
              />
            ))}
          </div>
        </div>

        {cfg.channels.length > 0 && (
          <div style={{ marginTop: "14px" }}>
            <label
              style={{
                fontSize: "10px",
                letterSpacing: ".08em",
                textTransform: "uppercase",
                color: "var(--dim)",
                display: "block",
                marginBottom: "8px",
              }}
            >
              channels
            </label>
            <div style={{ display: "flex", flexWrap: "wrap", gap: "7px" }}>
              {cfg.channels.map((c) => (
                <Chip
                  key={c.name}
                  label={c.name}
                  on={chans.includes(c.name)}
                  onClick={() => toggle(chans, c.name, setChans)}
                />
              ))}
            </div>
          </div>
        )}
      </div>

      <div className="actions">
        <Button onClick={submit} disabled={!name.trim()}>
          + add rule
        </Button>
        {msg && (
          <span className="sub" style={{ marginLeft: "12px", color: msg === "saved" ? "var(--teal)" : "var(--rose)" }}>
            {msg}
          </span>
        )}
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

function SettingsSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [cooldown, setCooldown] = useState(cfg.settings.cooldown_seconds);
  const [recovery, setRecovery] = useState(!cfg.settings.suppress_recovery);
  const [coalesce, setCoalesce] = useState(cfg.settings.coalesce_window_seconds ?? 10);
  const [overrides, setOverrides] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {};
    for (const ev of EVENT_TYPES) {
      const v = cfg.settings.cooldown_overrides?.[ev];
      init[ev] = v === undefined ? "" : String(v);
    }
    return init;
  });
  const [msg, setMsg] = useState("");

  async function save() {
    const co: Record<string, number> = {};
    for (const ev of EVENT_TYPES) {
      if (overrides[ev] !== "") co[ev] = Number(overrides[ev]);
    }
    await putNotifSettings({
      cooldown_seconds: cooldown,
      suppress_recovery: !recovery,
      cooldown_overrides: co,
      coalesce_window_seconds: coalesce,
    });
    setMsg("saved");
    onChange();
  }

  return (
    <>
      <SectionHeader index="03" title="Settings" />

      <div style={{ padding: "4px 20px 0" }}>
        {/* Recovery toggle */}
        <Toggle
          on={recovery}
          onChange={setRecovery}
          label="Send recovery notices"
          desc="Notify when a crashed process or downed agent comes back."
        />

        {/* Global cooldown + coalesce window */}
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "1fr 1fr",
            gap: "18px",
            padding: "16px 0",
            borderBottom: "1px solid var(--line2)",
            maxWidth: "560px",
          }}
        >
          <Field label="Global cooldown (seconds)">
            <Input
              type="number"
              value={cooldown}
              onChange={(e) => setCooldown(Number(e.target.value))}
            />
          </Field>
          <Field label="Coalesce window (sec · 0 = off)">
            <Input
              type="number"
              value={coalesce}
              onChange={(e) => setCoalesce(Number(e.target.value))}
            />
          </Field>
        </div>

        {/* Per-event cooldown overrides */}
        <div style={{ padding: "16px 0 6px" }}>
          <label
            style={{
              fontSize: "10px",
              letterSpacing: ".08em",
              textTransform: "uppercase",
              color: "var(--dim)",
            }}
          >
            Per-event cooldown overrides
          </label>
        </div>
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(3, 1fr)",
            gap: "12px",
            paddingBottom: "16px",
            maxWidth: "700px",
          }}
        >
          {EVENT_TYPES.map((ev) => (
            <Field key={ev} label={ev}>
              <Input
                type="number"
                placeholder={`${cooldown} (global)`}
                value={overrides[ev]}
                onChange={(e) =>
                  setOverrides({ ...overrides, [ev]: e.target.value })
                }
              />
            </Field>
          ))}
        </div>
      </div>

      <div className="actions">
        <Button onClick={save}>save settings</Button>
        {msg && (
          <span className="sub" style={{ marginLeft: "12px", color: "var(--teal)" }}>
            {msg}
          </span>
        )}
      </div>
    </>
  );
}
