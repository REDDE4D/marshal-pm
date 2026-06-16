# Fleet Process Manager — Architecture Design

**Date:** 2026-06-16
**Status:** Approved (architecture spec); sub-project specs to follow
**Name:** **Marshal** (CLI/binary: `marshal`) — to *marshal* = to organize and command forces.

## 1. Purpose & Motivation

A free, self-hosted alternative to PM2 — and specifically to **PM2 Plus / Keymetrics**,
whose monitoring dashboard, real-time metrics, alerting, and historical insights are
paywalled. This project delivers the process-management core **and** the "full insights"
layer for free.

Target user: someone running a **self-hosted fleet** — many hosts, monitored and
controlled from one central place. The tool is **language-agnostic**: it supervises any
kind of OS process, not just Node apps.

**Non-goals (v1):** SaaS hosting, HA/clustered central server, cert-authority-based
provisioning, external metric/log stores. These may come later but are explicitly out of
scope for the initial build.

## 2. Cross-Cutting Decisions (Locked)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Implementation language | **Go** | Single static binary, no runtime dependency, great for a system daemon, cross-platform. |
| Agent↔server transport | **Agent-initiated gRPC bidirectional stream** | One outbound long-lived connection per agent: metrics flow up, commands flow down the same channel. NAT/firewall friendly, no inbound ports on hosts. |
| Server storage | **Embedded: SQLite (metrics) + rotated local files (logs)** | Zero external dependencies; server is one binary + a data dir. Fits the "free & simple to self-host" goal. |
| Auth & security | **Bootstrap token + TLS** for agents; **username/password sessions** for dashboard; TLS everywhere | Pragmatic, ships fast, secure enough for v1. |
| License | **MIT or Apache-2.0 (fully free)** | Everything PM2 paywalls is free here; maximizes adoption and trust. |
| Build order | **(1) agent core → (2) metrics+log pipeline → (3) central server → (4) dashboard** | Bottom-up; every layer is real and useful before the next is built. |

## 3. System Overview

Three-tier, built bottom-up:

```
   Host A            Host B            Host C
 ┌────────┐        ┌────────┐        ┌────────┐
 │ Agent  │        │ Agent  │        │ Agent  │   ← supervises processes,
 │ +procs │        │ +procs │        │ +procs │     collects metrics/logs
 └───┬────┘        └───┬────┘        └───┬────┘
     │ outbound gRPC stream (metrics ↑ / commands ↓), token + TLS
     └────────────────┼────────────────┘
                      ▼
              ┌───────────────┐
              │ Central server│  ← aggregates state, SQLite + log files,
              │  (1 binary)   │     auth, REST/gRPC API, serves dashboard
              └───────┬───────┘
                      │ HTTPS (session auth)
                      ▼
              ┌───────────────┐
              │ Web dashboard │  ← "full insights", free
              └───────────────┘
```

## 4. Components & Responsibilities

### 4.1 Agent (one per host, Go binary)
The supervisor core and the part that directly replaces PM2's core.

- Spawn processes; enforce keep-alive and restart policies (e.g. on-failure, always,
  backoff, max-restarts).
- Capture each managed process's stdout/stderr.
- Sample resource metrics per process: CPU %, memory, uptime, restart count, status.
- **Persist its own process list to disk** so managed processes survive a daemon
  restart or host reboot.
- Hold the single outbound gRPC stream to the central server (when configured).
- Expose a **local CLI/API** so the agent is fully usable standalone — the server is
  additive and **never required**.

### 4.2 Central server (Go binary)
The control plane + data plane for the fleet.

- Terminate agent streams; track which agents are connected and their live state.
- Store metrics in SQLite (time-series) and logs in rotated files.
- Keep current fleet state in memory for fast dashboard reads.
- Expose a REST/gRPC API for the dashboard and the CLI.
- Handle auth: agent bootstrap tokens + per-agent identity; dashboard sessions.
- Route control commands down to the correct agent over its existing stream, after
  server-side authorization.
- Single binary + data directory.

### 4.3 Web dashboard (separate sub-project, built last)
The free "full insights" UI.

- Live process list across all hosts.
- Metric charts (CPU/mem/uptime/restarts over time).
- Live log tailing.
- Process controls: start / stop / restart per process per host.

### 4.4 CLI
- Primary interface for the local agent (works with no server present).
- Can also target the central server for fleet-wide commands.

## 5. Data & Control Flow

- **Up (data plane):** agent samples metrics on an interval → batches → streams to the
  server → SQLite (metrics) + log files (logs). Server holds current state in memory for
  fast reads.
- **Down (control plane):** dashboard/CLI issues e.g. `restart <app> on <host>` → server
  validates auth → pushes the command over that host's existing stream → agent executes
  → streams result and new state back up.
- **Standalone mode:** an agent with no server configured still works fully via local CLI.
  The server layer is purely additive.

## 6. Security

- Agents authenticate with a **bootstrap token over TLS**; each agent is assigned an
  identity on first connect.
- Dashboard uses **username/password → server-side sessions**.
- **All transport is TLS.**
- Control commands are **authorized server-side** before being routed to an agent.

## 7. Contracts This Spec Defines

So each sub-project can be built independently, the architecture fixes these contracts
(to be detailed in the sub-project specs):

- **The gRPC service** between agent and server (stream messages: metric batches, log
  batches, state updates upward; commands and acks downward).
- **The data model** for processes, metrics samples, and log records.
- **The auth model** (token bootstrap, agent identity, dashboard sessions).

## 8. Sub-Project Roadmap

| # | Sub-project | Depends on | Independently useful? |
|---|-------------|-----------|----------------------|
| 1 | Agent / supervisor core (+ CLI) | — | Yes — a working PM2-core replacement on one host. |
| 2 | Metrics & log pipeline | #1 | Yes — local insights on one host. |
| 3 | Central server / fleet aggregation | #1, #2 | Yes — multi-host control & storage. |
| 4 | Web dashboard | #3 | Yes — the full free insights UI. |

Each sub-project gets its own spec → plan → implementation cycle. **Next step: brainstorm
sub-project #1 (the agent / supervisor core) into a full design.**
