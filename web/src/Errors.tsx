import { useEffect, useState } from "react";
import { getErrors, type ErrorsResponse } from "./api";
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

const LEDGER_COLS = "26px 2fr 1fr 1.3fr 0.7fr 0.8fr";

export function Errors() {
  const [range, setRange] = useState<Range>("24h");
  const [data, setData] = useState<ErrorsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setErr(null);
    setData(null);
    getErrors(range)
      .then((d) => { if (live) setData(d); })
      .catch((e) => { if (live) setErr(String(e)); });
    return () => { live = false; };
  }, [range]);

  // Derive most-recent signature for "Last error" sub label
  const mostRecentSig = data?.signatures.reduce<typeof data.signatures[0] | null>(
    (best, s) => (!best || s.last_unix > best.last_unix ? s : best),
    null
  ) ?? null;

  const lastSub = mostRecentSig
    ? `ago · ${mostRecentSig.agent} / ${mostRecentSig.proc}`
    : "ago";

  return (
    <>
      {/* Cluster */}
      <MetricCluster cols={4}>
        <Cell
          label={`Errors ${range}`}
          value={data ? data.cluster.errors : "—"}
          sub={rangeLabel(range)}
          color="rose"
        />
        <Cell
          label="Distinct"
          value={data ? data.cluster.signatures : "—"}
          sub="error signatures"
          color="amber"
        />
        <Cell
          label="Affected procs"
          value={data ? data.cluster.affected_procs : "—"}
          sub="processes"
        />
        <Cell
          label="Last error"
          value={data ? relativeTime(data.cluster.last_error_unix) : "—"}
          sub={data ? lastSub : undefined}
          color="sky"
        />
      </MetricCluster>

      {/* Section header */}
      <SectionHeader
        index="01"
        title="Exceptions"
        right={
          <Segment<Range>
            options={RANGE_OPTIONS}
            value={range}
            onChange={setRange}
          />
        }
        count={data ? `${data.signatures.length} signatures` : undefined}
      />

      {/* States */}
      {err && <p className="sub" style={{ padding: "12px 22px", color: "var(--rose)" }}>Failed to load: {err}</p>}
      {!err && !data && <p className="sub" style={{ padding: "12px 22px" }}>Loading…</p>}

      {data && (
        <>
          {data.truncated && (
            <p className="sub" style={{ padding: "6px 22px" }}>
              Showing a partial window — scan cap reached.
            </p>
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
              </LedgerHeader>

              {data.signatures.map((sig, i) => (
                <LedgerRow
                  key={sig.id}
                  cols={LEDGER_COLS}
                  onClick={() => navigate(procHref(sig.agent, sig.proc))}
                >
                  <div className="ix">{String(i + 1).padStart(2, "0")}</div>
                  <div>
                    <div className="nm rose" style={{ fontSize: "12px" }}>{sig.sample}</div>
                    <div className="sub">{sig.source ?? "—"}</div>
                  </div>
                  <div className="v" style={{ fontSize: "11px", color: "var(--dim)" }}>
                    {sig.agent} / {sig.proc}
                    {sig.affected.length > 1 && ` · ${sig.affected.length} procs`}
                  </div>
                  <BarSparkline points={sig.buckets} color="var(--rose)" />
                  <div className="rr v rose">{sig.count}</div>
                  <div className="rr v" style={{ fontSize: "11px", color: "var(--dim)" }}>
                    {relativeTime(sig.last_unix)}
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
