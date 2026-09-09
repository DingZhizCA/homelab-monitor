# Home Lab Monitor — Phase 1 Specification

**Project:** `homelab-monitor`  
**Phase:** 1 — End-to-End Real-Time Monitoring Slice  
**Status:** Draft for implementation review  
**Primary environment:** Linux / Ubuntu Server  

## 1. Objective

Phase 1 must deliver a complete, runnable vertical slice:

```text
homelab-agent
    │
    │ HTTP POST /api/v1/metrics (JSON, adaptive interval)
    ▼
homelab-server
    │
    ├── validate metrics
    ├── keep latest snapshot in memory
    ├── calculate device status
    ├── REST API
    └── SSE broadcast
    │
    ▼
React Dashboard
```

The purpose of Phase 1 is to prove the complete Agent → Server → Browser path before introducing persistent time-series storage or other infrastructure.

Phase 1 intentionally stores only the latest state in server memory. Historical data is not durable and is lost when the server restarts.

## 2. Technology Baseline

### Backend / Agent

- Go
- Two binaries from one repository:
  - `homelab-server`
  - `homelab-agent`
- HTTP + JSON for Agent → Server transport
- Server-Sent Events (SSE) for Server → Browser real-time updates
- systemd for production service management
- stdout/stderr logging, collected by journald

### Frontend

- React
- TypeScript
- Vite
- Tailwind CSS
- shadcn/ui
- Apache ECharts

The UI must be responsive and support phone, tablet, 1080p, 1440p/2K, and 4K layouts without maintaining separate applications.

## 3. Explicit Phase 1 Non-Goals

Do **not** add the following in Phase 1 unless this specification is revised first:

- PostgreSQL
- VictoriaMetrics
- Redis
- Docker as a runtime requirement
- Prometheus
- Grafana
- user login system
- multi-tenancy
- complex authorization
- alerting engine
- WebSocket
- persistent historical metrics
- offline Agent queue/buffer
- detailed SMART collection
- HDD temperature polling
- general/default HDD power-state polling; Phase 1 only permits an explicitly enabled,
  low-frequency ATA CHECK POWER MODE query for confirmed direct ATA/SATA HDDs
- Load_Cycle_Count polling

Avoid speculative abstractions for these future features.

## 4. Design Principles

1. Build for the current Home Lab first, while avoiding hard-coded machine-specific assumptions.
2. IP addresses are transport details, not device identities.
3. Agent failure or Server unavailability must not crash the other component.
4. A monitoring collector must not negatively affect the system it monitors.
5. In particular, future HDD monitoring must never wake a sleeping HDD solely to obtain monitoring data.
6. Prefer Linux kernel interfaces (`/proc`, `/sys`, syscalls) over repeatedly spawning shell commands when practical.
7. Keep Phase 1 small enough to inspect and test thoroughly.
8. API structures must be versioned.

## 5. Device Identity

A device has three distinct identity/display fields:

| Field | Meaning |
|---|---|
| `device_id` | Stable, unique, machine-readable identifier configured by the operator |
| `display_name` | Human-readable UI name |
| `hostname` | Current hostname reported by the operating system |

Example:

```json
{
  "device_id": "alienware",
  "display_name": "Alienware 17 R3",
  "hostname": "alienware"
}
```

Another device may use:

```json
{
  "device_id": "n1",
  "display_name": "Phicomm N1",
  "hostname": "armbian"
}
```

`device_id` must not be derived from the current IPv4 address.

Phase 1 uses explicitly configured device IDs. Automatic enrollment/registration is outside Phase 1.

## 6. Agent Configuration

Recommended YAML structure:

```yaml
server:
  url: "http://192.168.1.3:8080"
  token: ""

agent:
  device_id: "alienware"
  display_name: "Alienware 17 R3"
  interval: 5s

collectors:
  cpu: true
  memory: true
  disk: true
  network: true
```

Production default path:

```text
/etc/homelab-monitor/agent.yaml
```

Development must allow an explicit configuration path, for example:

```bash
homelab-agent --config ./configs/agent.example.yaml
```

Do not require developers to modify `/etc` for ordinary local testing.

## 7. Sampling

Agent sampling uses two modes:

```text
active: 5 seconds
idle:   60 seconds
```

The active and idle intervals must be configurable. A visible Dashboard sends a
presence heartbeat; the Server keeps active mode for 30 minutes after the last
heartbeat, then returns to idle mode. Server restart, missing presence, or an
expired instruction defaults to idle mode.

Phase 1 sends one current snapshot per sampling cycle.

## 8. Metrics Envelope

Agent metrics are sent to:

```text
POST /api/v1/metrics
```

The payload must use a versioned envelope.

Example:

```json
{
  "schema_version": 1,
  "device": {
    "id": "alienware",
    "display_name": "Alienware 17 R3",
    "hostname": "alienware"
  },
  "timestamp": "2026-09-02T16:30:00+08:00",
  "system": {
    "uptime_seconds": 86400,
    "load1": 0.12,
    "load5": 0.08,
    "load15": 0.06
  },
  "cpu": {
    "usage_percent": 12.4,
    "logical_cores": 8,
    "frequency_mhz": 2800,
    "temperature_celsius": 58.0
  },
  "memory": {
    "total_bytes": 17179869184,
    "used_bytes": 4294967296,
    "available_bytes": 12884901888,
    "usage_percent": 25.0
  },
  "disks": [],
  "networks": []
}
```

`schema_version` is mandatory. Phase 1 supports version `1`.

## 9. System Metrics

Phase 1 system metrics:

- hostname
- uptime
- load average (1, 5, 15 minutes)
- Agent sample timestamp

The Agent supplies the sample timestamp.

## 10. CPU Metrics

Phase 1 CPU metrics:

- total CPU usage percentage
- logical CPU count
- current/representative CPU frequency when available
- CPU temperature when available through safe Linux interfaces

A missing or unsupported optional sensor must not cause the entire sample to fail.

CPU usage must be based on CPU time deltas rather than treating a single cumulative `/proc/stat` reading as an instantaneous percentage.

Detailed collector algorithms and Linux data-source rules should be specified before implementation of the collector layer.

## 11. Memory Metrics

Phase 1 memory metrics:

- total bytes
- used bytes
- available bytes
- usage percentage

Linux semantics must be defined consistently. Prefer the kernel's `MemAvailable` concept when available rather than interpreting all cache as permanently consumed memory.

## 12. Disk Metrics

A machine may expose multiple filesystems/disks, therefore disk data is an array.

Example element:

```json
{
  "device": "/dev/nvme0n1",
  "mount_point": "/",
  "filesystem": "ext4",
  "total_bytes": 128000000000,
  "used_bytes": 32000000000,
  "available_bytes": 96000000000,
  "usage_percent": 25.0
}
```

Phase 1 disk scope:

- filesystem/device identity when safely discoverable
- mount point
- filesystem type
- total capacity
- used capacity
- available capacity
- usage percentage
- safely available sysfs identity metadata (media type, physical device, vendor, and model)

An eligible mount may be reported before capacity is available. In that case the
Dashboard must identify the capacity read as deferred by the low-power policy;
it must not claim that the HDD is confirmed to be sleeping.

Phase 1 does **not** poll:

- SMART attributes
- HDD temperature
- automatic power-state queries on transports other than explicitly enabled direct ATA/SATA
- Load_Cycle_Count
- Start_Stop_Count
- pending/uncorrectable sectors

### HDD safety requirement

Future disk-health collectors must obey this invariant:

> A monitoring operation must not wake an HDD that is currently in standby solely to obtain a metric.

This is a project-level requirement, not an optional optimization.

## 13. Network Metrics

Network data is an array because systems can contain physical and virtual interfaces.

Example:

```json
{
  "interface": "enp59s0",
  "rx_bytes": 123456789,
  "tx_bytes": 98765432,
  "rx_bytes_per_second": 125000,
  "tx_bytes_per_second": 32000
}
```

Phase 1 metrics:

- interface name
- cumulative RX bytes
- cumulative TX bytes
- RX bytes/second
- TX bytes/second

Rate values must be calculated from counter deltas over elapsed time.

Loopback (`lo`) should be filtered by default.

The implementation must not assume the primary interface is named `eth0`; names such as `enp59s0`, `tailscale0`, bridges, and other interfaces are valid.

Virtual-interface inclusion policy may become configurable later.

## 14. Time Semantics

Agent sample timestamps use RFC 3339.

Example:

```text
2026-09-02T16:30:00+08:00
```

The Server additionally records its own receipt time:

```text
received_at
```

This distinction permits later detection of clock skew, delayed delivery, or stale samples.

Do not overwrite the Agent sample timestamp with Server receipt time.

## 15. Server Responsibilities

`homelab-server` Phase 1 responsibilities:

1. expose the HTTP API;
2. decode incoming JSON;
3. validate supported schema version;
4. validate required device and metric fields;
5. store the latest valid snapshot for each device in memory;
6. record `received_at`;
7. calculate Online/Stale/Offline state;
8. expose latest device state through REST;
9. broadcast accepted metric updates through SSE;
10. provide health endpoint;
11. log relevant operational errors without terminating unnecessarily.

The Server does not persist historical metrics in Phase 1.

## 16. REST API

Phase 1 endpoints:

```text
GET  /api/v1/health
POST /api/v1/metrics
GET  /api/v1/devices
GET  /api/v1/devices/{device_id}
GET  /api/v1/events
POST /api/v1/presence
POST /api/v1/devices/{device_id}/refresh
GET  /api/v1/refresh/{request_id}
```

### 16.1 Health

```text
GET /api/v1/health
```

Minimum response:

```json
{
  "status": "ok"
}
```

This endpoint is intended for local diagnostics, service checks, and future reverse-proxy/monitoring integration.

### 16.2 Metrics ingestion

```text
POST /api/v1/metrics
```

Processing pipeline:

```text
HTTP request
   ↓
JSON decode
   ↓
schema-version validation
   ↓
field/device validation
   ↓
update latest in-memory snapshot
   ↓
record received_at
   ↓
broadcast SSE metrics event
```

The endpoint should acknowledge accepted samples without performing unnecessary long-running work in the request path.

### 16.3 Device list

```text
GET /api/v1/devices
```

Returns devices known to the running Server and enough latest-state/status information for the Dashboard device list.

### 16.4 Device latest state

```text
GET /api/v1/devices/{device_id}
```

Returns the latest accepted snapshot and server-derived status for one device.

Unknown IDs return `404 Not Found`.

## 17. Device Liveness

The Agent must not send a self-declared boolean such as:

```json
{"online": true}
```

Liveness is calculated by the Server from `last_seen` / `received_at`.

Liveness thresholds follow the sampling interval reported by the Agent:

| Time since last accepted sample | Status |
|---|---|
| 0–3 × sampling interval | `online` |
| >3–12 × sampling interval | `stale` |
| >12 × sampling interval | `offline` |

These thresholds should be represented centrally rather than duplicated throughout handlers/UI code.

## 18. Server-Sent Events

Endpoint:

```text
GET /api/v1/events
```

Phase 1 event type:

```text
event: metrics
data: {...}
```

The browser uses native `EventSource` behavior and automatic reconnection.

Future event names may include:

- `device_status`
- `alert`
- `server_status`

They are not required in Phase 1.

SSE is preferred over WebSocket in Phase 1 because the real-time path is primarily Server → Browser and does not require a bidirectional socket protocol.

## 19. API Error Format

API errors use one consistent JSON envelope.

Example:

```json
{
  "error": {
    "code": "INVALID_METRICS",
    "message": "cpu.usage_percent must be between 0 and 100"
  }
}
```

Expected HTTP statuses include at least:

- `200 OK`
- `202 Accepted`
- `400 Bad Request`
- `401 Unauthorized` (reserved/usable when Agent authentication is enabled)
- `404 Not Found`
- `405 Method Not Allowed`
- `500 Internal Server Error`

Do not return arbitrary plain-text error formats from individual handlers.

## 20. Agent Failure Behavior

Server unavailability must not terminate `homelab-agent`.

Expected loop:

```text
collect metrics
    ↓
POST metrics
    ↓
request fails
    ↓
log warning
    ↓
wait until next sampling cycle
    ↓
retry
```

Phase 1 does not persist unsent metrics locally.

Therefore, metrics generated while the Server is unavailable may be lost. This is accepted Phase 1 behavior.

The HTTP client must use finite timeouts so a broken Server/network cannot block future sampling indefinitely.

## 21. Permissions

### Server

`homelab-server` should run as an unprivileged service account/user and must not require root for normal operation.

### Agent

Phase 1 should also run the Agent without root whenever the required metrics are readable through normal Linux interfaces.

Do not make the entire Agent root merely to simplify future collectors.

Future privileged collectors must be designed with narrowly scoped privileges/capabilities or controlled helper mechanisms where practical.

## 22. Logging

Both binaries log to stdout/stderr.

systemd/journald owns collection and retention.

Example conceptual fields:

```text
time=...
level=INFO
component=agent
device=alienware
msg="metrics sent"
```

Server uses `component=server`.

The application must not create its own mandatory `/var/log/homelab-monitor/*.log` logging subsystem in Phase 1.

Operational inspection should work with commands such as:

```bash
journalctl -u homelab-agent
journalctl -u homelab-server
```

## 23. Repository Layout

Target structure:

```text
homelab-monitor/
├── cmd/
│   ├── server/
│   │   └── main.go
│   └── agent/
│       └── main.go
├── internal/
│   ├── agent/
│   │   ├── collector.go
│   │   ├── cpu.go
│   │   ├── memory.go
│   │   ├── disk.go
│   │   └── network.go
│   ├── api/
│   ├── metrics/
│   └── server/
├── web/
│   └── ...
├── configs/
├── deployments/
│   └── systemd/
├── docs/
├── go.mod
├── go.sum
├── README.md
└── .gitignore
```

Do not create empty abstraction layers solely to match the diagram; packages should exist when they have a concrete responsibility.

## 24. Build Model

Go builds the two native binaries directly:

```bash
go build -o bin/homelab-server ./cmd/server
go build -o bin/homelab-agent ./cmd/agent
```

No CMake, GN, or Ninja is required for this project.

The frontend is built separately through Vite/Node tooling and produces static assets under a directory such as:

```text
web/dist/
```

A later implementation may embed the production frontend assets into `homelab-server`, allowing the Server binary to serve both the API and Dashboard. This must not complicate the initial backend implementation.

A root-level Makefile may later provide convenience commands, but it is an orchestration wrapper rather than the underlying build system.

## 25. Frontend Phase 1 Requirements

The first Dashboard must show at least:

- overall device health/status
- CPU usage
- memory usage
- CPU temperature when available
- uptime
- CPU usage real-time chart
- memory state
- network RX/TX rates
- device list

### Responsive behavior

One application must adapt to:

- phones (~375 px class)
- tablets (~768 px class)
- 1080p desktop
- 1440p / 2K desktop
- 4K desktop

Requirements:

- no desktop-only fixed-width layout;
- no mandatory horizontal page scrolling on phone layouts;
- metric cards reflow according to available width;
- charts resize with their containers;
- touch interaction must remain usable;
- support iPhone Safari portrait and landscape;
- account for mobile safe areas where relevant;
- support dark mode.

Tailwind CSS owns layout/responsive styling; shadcn/ui supplies reusable UI primitives; Apache ECharts owns metric visualization.

## 26. Browser Real-Time Buffer

Phase 1 does not have durable historical storage.

For live charts, the browser may maintain a bounded rolling buffer, initially targeting approximately the latest five minutes of samples.

With a 5-second sampling interval this is approximately 60 samples per metric series.

The buffer must be bounded; the UI must not append samples indefinitely.

Phase 2 historical views (for example 24h, 7d, 30d) will query the time-series backend rather than relying on the browser session buffer.

## 27. Authentication Boundary

The Agent configuration reserves a `server.token` field so the protocol can support a pre-shared credential without redesigning configuration later.

Phase 1 implementation should not introduce a complex authentication/user system.

If token authentication is implemented during Phase 1, it should remain simple and narrowly scoped to Agent ingestion. Do not build user accounts, sessions, OAuth, RBAC, or multi-tenant authorization.

Secrets must not be committed to Git.

## 28. Initial Deployment Model

Expected first deployment:

```text
Alienware Ubuntu Server
├── homelab-server
├── homelab-agent
└── Web Dashboard

Phicomm N1 (later in rollout)
└── homelab-agent
```

The architecture must permit additional Linux devices to run the same Agent with different configuration.

Machine names, IP addresses, and hardware models must not be hard-coded into Go source.

## 29. Phase 1 Acceptance Criteria

Phase 1 is complete only when all of the following are demonstrably true:

1. `homelab-agent` builds successfully.
2. `homelab-server` builds successfully.
3. Agent starts with an explicit YAML configuration.
4. Agent safely collects the defined Phase 1 Linux metrics.
5. Agent sends a schema-v1 JSON snapshot every configured interval.
6. Temporary Server/network failure does not terminate the Agent.
7. Server validates incoming data and keeps the latest snapshot per device in memory.
8. `/api/v1/health` responds correctly.
9. `/api/v1/devices` returns known devices.
10. `/api/v1/devices/{device_id}` returns the latest state.
11. Server calculates `online`, `stale`, and `offline` from receipt time.
12. `/api/v1/events` delivers real-time metric events via SSE.
13. React Dashboard receives initial state through REST and subsequent updates through SSE.
14. Dashboard displays CPU, memory, temperature when available, uptime, network rates, and device status.
15. Live chart data uses a bounded rolling buffer.
16. UI works at phone, tablet, 1080p, 2K, and 4K classes.
17. Dark mode works.
18. Agent and Server can run under systemd without requiring interactive terminals.
19. Logs are visible through journald.
20. No Phase 1 collector wakes a sleeping HDD merely to collect monitoring information.

## 30. Implementation Order

Codex/implementation work should proceed incrementally:

1. finalize Go metric structs and protocol types;
2. define collector interfaces and Linux data-source behavior;
3. implement System/CPU/Memory collectors;
4. implement safe filesystem and network collectors;
5. implement Agent configuration and sampling loop;
6. implement Agent HTTP sender with timeout/error handling;
7. implement Server in-memory state store;
8. implement REST endpoints and validation;
9. implement liveness calculation;
10. implement SSE broadcaster;
11. add backend tests;
12. create React/Vite frontend skeleton;
13. add Tailwind CSS and UI primitives;
14. implement REST initial load;
15. implement SSE live updates;
16. implement ECharts live graphs;
17. implement responsive layouts/dark mode;
18. add systemd deployment units;
19. perform end-to-end acceptance testing;
20. only then prepare the first implementation milestone commit.

Do not generate the entire application in one unreviewed step.

## 31. Next Design Task Before Collector Implementation

Before implementing the Linux collector layer, define and review the exact data sources and algorithms for:

- CPU usage from `/proc/stat` deltas;
- CPU frequency source/fallback behavior;
- CPU temperature source and sensor selection;
- memory calculations from `/proc/meminfo`;
- filesystem enumeration and capacity calculation;
- network counters and rate calculations;
- counter reset/interface disappearance behavior;
- unsupported/missing sensor semantics.

This collector specification should be approved before Codex writes production collector code.
