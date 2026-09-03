# Phase 1 Linux Collector Design

Status: proposed for review before production collector implementation.

## General behavior

- Read Linux kernel interfaces directly; do not run recurring shell commands.
- A sampling cycle uses one wall-clock timestamp after collection completes.
- Failure of an optional metric (CPU frequency or temperature) produces JSON `null`; it does not fail the sample.
- Failure of one disk or network entry skips that entry and logs a warning. Failure of required system, CPU-time, or memory data fails that sampling cycle without terminating the Agent.
- Apply a lowest-power collection policy: lightweight `/proc` counters follow the Agent interval, CPU frequency/temperature default to one minute, and filesystem capacity defaults to five minutes. The slower values are cached between reads.
- The Agent defaults to 60-second idle sampling. A visible Dashboard heartbeat selects 5-second active sampling; active mode remains for 30 minutes after the final heartbeat. Cached metric timestamps do not change merely because a new envelope is sent.
- Read hostname and logical CPU count once at startup. Do not emit a success log for every sample; repeated collection and transport errors are rate-limited to one log per minute per category.

## System

- Hostname: `os.Hostname()`.
- Uptime: first numeric field of `/proc/uptime`, truncated to whole seconds.
- Load: the first three fields of `/proc/loadavg`.

## CPU

- Read the aggregate `cpu` row in `/proc/stat`. Total time is the sum of all reported counters. Idle time is `idle + iowait`.
- Usage over two consecutive reads is `100 * (deltaTotal - deltaIdle) / deltaTotal`.
- The first read establishes a baseline. Collection waits until the next configured sampling tick; it does not add a second artificial sleep.
- If counters decrease, or `deltaTotal` is zero, discard the rate for that cycle and establish a new baseline.
- Logical cores: `runtime.NumCPU()`.
- Frequency: average readable values from `/sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq` (kHz). Fall back to the average `cpu MHz` values in `/proc/cpuinfo`. Missing data maps to `null`.
- Temperature: inspect `/sys/class/hwmon/hwmon*/name`, preferring sensor names `coretemp`, `k10temp`, `zenpower`, then `cpu_thermal`. Within the selected sensor, prefer labels containing `Package`, `Tctl`, or `CPU`; otherwise use the highest valid `temp*_input`. Millidegrees are converted to Celsius. Values outside -20–150 °C are ignored.

## Memory

- Parse `/proc/meminfo` values as KiB and multiply by 1024 with overflow checks.
- `total = MemTotal` and `available = MemAvailable`.
- On kernels without `MemAvailable`, use `MemFree + Buffers + Cached + SReclaimable - Shmem`, clamped to `[0, total]`.
- `used = total - available`; usage is `100 * used / total`.

## Filesystems

- Enumerate `/proc/self/mountinfo` and resolve each mount's major/minor number through `/sys/dev/block`.
- Recursively inspect the sysfs block-device `slaves` graph and `queue/rotational`. Only call `statfs` when every leaf device is positively identified as non-rotational. Skip rotational devices and devices whose media type cannot be established.
- Exclude pseudo/API filesystems (`proc`, `sysfs`, `devtmpfs`, `devpts`, `tmpfs`, `cgroup`, `cgroup2`, `securityfs`, `debugfs`, `tracefs`, `configfs`, `pstore`, `mqueue`, `hugetlbfs`, `fusectl`, `binfmt_misc`, `autofs`).
- Deduplicate by mount point and sort output by mount point.
- Capacity uses filesystem blocks: `total = Blocks * Bsize`, `available = Bavail * Bsize`, and `used = total - Bfree * Bsize`. Usage is `used / (used + available)` when the denominator is nonzero, matching non-root usable-capacity semantics.
- Reading mount metadata, block-layer counters, and sysfs does not issue media I/O. Normal sampling never calls `statfs` on an idle HDD, invokes SMART tools, reads drive health/temperature, or probes block devices. HDD capacity may be absent until business activity or an explicit user-approved refresh occurs.
- Normal sampling passively reads each HDD's block-layer `in_flight` counter. It refreshes filesystem capacity only when business I/O is currently in flight; otherwise it retains the last value and its original `collected_at` timestamp.
- An explicit `POST /api/v1/devices/{device_id}/refresh` request with `{"collectors":["disk"],"allow_wake":true}` queues a one-shot active refresh and returns a `request_id`. The Agent may wake the disk for this user-approved operation, labels returned values `collection_mode: "active"`, and immediately reports them. `GET /api/v1/refresh/{request_id}` reports `queued`, `delivered`, `completed`, `failed`, or `expired` for Dashboard feedback.

## Network

- Read `/proc/net/dev`; include every interface except `lo` by default.
- RX bytes are field 1 and TX bytes field 9 after the interface name.
- Rates use monotonic elapsed time and consecutive counter deltas per interface.
- A new interface has zero rates until its second sample. If a counter decreases, rates are zero and that interface's baseline is reset. Disappeared interfaces are removed from baseline state.
- Output is sorted by interface name.

## Open review point

The protocol models CPU and memory as required objects. Disabling either is a configuration error; disk and network may be disabled and then send empty arrays. This avoids ambiguous zero-valued required metrics.
