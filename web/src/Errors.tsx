import { useEffect, useState } from "react";
import { getErrors, ackError, type ErrorsResponse, type ErrSignature } from "./api";
import { MetricCluster, Cell } from "./components/Cluster";
import { SectionHeader, LedgerHeader, LedgerRow } from "./components/Ledger";
import { Segment } from "./components/Controls";
import { BarSparkline } from "./components/BarSparkline";
import { relativeTime } from "./lib/format";
import { navigate, procHref } from "./router";

type Range = "24h" | "7d" | "all";

const RANGE_OPTIONS: { value: Range; label: string }[] = [
  { value: "all", label: "all" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
];

function rangeLabel(range: Range): string {
  if (range === "24h") return "in 24h";
  if (range === "7d") return "in 7d";
  return "all time";
}

const LEDGER_COLS = "26px 2fr 1fr 1.3fr 0.6fr 0.7fr 0.7fr";

export function Errors() {
  const [range, setRange] = useState<Range>("24h");
  const [data, setData] = useState<ErrorsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let live = true;
    setErr(null);
    getErrors(range)
      .then((d) => { if (live) setData(d); })
      .catch((e) => { if (live) setErr(String(e)); });
    return () => { live = false; };
  }, [range, tick]);

  const [acking, setAcking] = useState(false);

  async function onAck(sig: ErrSignature) {
    try {
      await ackError(sig.id, !sig.acknowledged);
      setTick((t) => t + 1); // reload so the badge + counts update
    } catch {
      /* swallow — non-fatal */
    }
  }

  const unacked = data?.signatures.filter((s) => !s.acknowledged) ?? [];

  async function onAckAll() {
    if (acking || unacked.length === 0) return;
    setAcking(true);
    try {
      await Promise.all(unacked.map((s) => ackError(s.id, true)));
    } catch {
      /* swallow — non-fatal; reload reflects whatever stuck */
    } finally {
      setAcking(false);
      setTick((t) => t + 1);
    }
  }

  const mostRecentSig = data?.signatures.reduce<ErrSignature | null>(
    (best, s) => (!best || s.last_unix > best.last_unix ? s : best),
    null
  ) ?? null;

  const lastSub = mostRecentSig ? `ago · ${mostRecentSig.agent} / ${mostRecentSig.proc}` : "ago";

  return (
    <>
      <MetricCluster cols={4}>
        <Cell label={`Errors ${range}`} value={data ? data.cluster.errors : "—"} sub={rangeLabel(range)} color="rose" />
        <Cell
          label="Unacked"
          value={data ? data.cluster.unacknowledged : "—"}
          sub={data ? `of ${data.cluster.signatures} signatures` : "error signatures"}
          color="amber"
        />
        <Cell label="Affected procs" value={data ? data.cluster.affected_procs : "—"} sub="processes" />
        <Cell
          label="Last error"
          value={data ? relativeTime(data.cluster.last_error_unix) : "—"}
          sub={data ? lastSub : undefined}
          color="sky"
        />
      </MetricCluster>

      <SectionHeader
        index="01"
        title="Exceptions"
        right={
          <div className="secctl">
            <button
              className="ackbtn"
              onClick={onAckAll}
              disabled={acking || unacked.length === 0}
              title="Acknowledge every unacknowledged exception in this window"
            >
              {acking ? "acking…" : `ack all${unacked.length > 0 ? ` · ${unacked.length}` : ""}`}
            </button>
            <Segment<Range> options={RANGE_OPTIONS} value={range} onChange={setRange} />
          </div>
        }
        count={data ? `${data.signatures.length} signatures` : undefined}
      />

      {err && <p className="sub" style={{ padding: "12px 22px", color: "var(--rose)" }}>Failed to load: {err}</p>}
      {!err && !data && <p className="sub" style={{ padding: "12px 22px" }}>Loading…</p>}

      {data && (
        <>
          {data.truncated && (
            <p className="sub" style={{ padding: "6px 22px" }}>Showing a partial window — scan cap reached.</p>
          )}

          {data.signatures.length === 0 ? (
            <p className="sub" style={{ padding: "12px 22px" }}>No errors in this window.</p>
          ) : (
            <>
              <LedgerHeader cols={LEDGER_COLS}>
                <div />
                <div>Message</div>
                <div>Source</div>
                <div>Occurrences</div>
                <div className="rr">Count</div>
                <div className="rr">Last</div>
                <div className="rr" />
              </LedgerHeader>

              {data.signatures.map((sig, i) => (
                <LedgerRow
                  key={sig.id}
                  cols={LEDGER_COLS}
                  onClick={() => navigate(procHref(sig.agent, sig.proc))}
                  dim={sig.acknowledged}
                >
                  <div className="ix">{String(i + 1).padStart(2, "0")}</div>
                  <div>
                    <div className="nm rose" style={{ fontSize: "12px" }}>{sig.sample}</div>
                    <div className="sub">{sig.source ?? "—"}</div>
                  </div>
                  <div className="v clip" style={{ fontSize: "11px", color: "var(--dim)" }}>
                    {sig.agent} / {sig.proc}
                    {sig.affected.length > 1 && ` · ${sig.affected.length} procs`}
                  </div>
                  <BarSparkline points={sig.buckets} color="var(--rose)" />
                  <div className="rr v rose">{sig.count}</div>
                  <div className="rr v" style={{ fontSize: "11px", color: "var(--dim)" }}>{relativeTime(sig.last_unix)}</div>
                  <div className="rr">
                    <button
                      className={`ackbtn${sig.acknowledged ? " on" : ""}`}
                      onClick={(e) => { e.stopPropagation(); onAck(sig); }}
                      title={sig.acknowledged ? "Un-acknowledge" : "Acknowledge — stops this error from nagging"}
                    >
                      {sig.acknowledged ? "acked" : "ack"}
                    </button>
                  </div>
                </LedgerRow>
              ))}
            </>
          )}
        </>
      )}
    </>
  );
}
