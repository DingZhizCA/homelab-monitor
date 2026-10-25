/**
 * Dashboard 使用的 Agent/Server 协议类型与浏览器派生类型。
 * 时间字符串均来自 JSON；字节速率单位为 bytes/second，百分比范围为 0～100。
 */

/** DeviceStatus is derived by Server from the latest receipt time. */
export type DeviceStatus = 'online' | 'stale' | 'offline'

/** Safe physical-device metadata associated with a mounted filesystem. */
export interface DiskDeviceInfo {
  /** Linux leaf block-device name without `/dev/`. */
  name: string
  /** Optional identity read from sysfs without media access. */
  model?: string
  vendor?: string
  /** Kernel-reported rotational flag. */
  rotational: boolean
  /** `unknown` means a query was attempted but did not produce a reliable state. */
  power_state?: 'active' | 'standby' | 'sleeping' | 'unknown'
  /** Time of the latest power-state query attempt, including unknown results. */
  power_state_collected_at?: string
}

/** Filesystem capacity plus safe metadata for its underlying block devices. */
export interface DiskMetric {
  device: string
  mount_point: string
  filesystem: string
  media_type?: 'hdd' | 'ssd'
  physical_devices?: DiskDeviceInfo[]
  /** False means capacity was deliberately deferred, not that the disk has zero size. */
  capacity_available?: boolean
  total_bytes: number
  used_bytes: number
  available_bytes: number
  usage_percent: number
  /** Original capacity collection time; cached values retain their old timestamp. */
  collected_at: string
  /** Empty while capacity has never been safely collected. */
  collection_mode: 'passive' | 'active' | ''
}

/** Cumulative interface counters and rates calculated from adjacent Agent samples. */
export interface NetworkMetric {
  interface: string
  rx_bytes: number
  tx_bytes: number
  rx_bytes_per_second: number
  tx_bytes_per_second: number
}

/** One complete Agent sample returned by Server. */
export interface Snapshot {
  schema_version: number
  sampling_mode: 'idle' | 'active'
  sampling_interval_seconds: number
  device: { id: string; display_name: string; hostname: string }
  /** Agent sampling time, distinct from DeviceState.received_at. */
  timestamp: string
  system: { uptime_seconds: number; load1: number; load5: number; load15: number }
  cpu: { usage_percent: number; logical_cores: number; frequency_mhz: number | null; temperature_celsius: number | null; sensor_collected_at: string | null }
  memory: { total_bytes: number; used_bytes: number; available_bytes: number; usage_percent: number }
  disks: DiskMetric[]
  networks: NetworkMetric[]
}

/** Latest snapshot decorated with Server receipt time and current liveness. */
export interface DeviceState {
  snapshot: Snapshot
  received_at: string
  status: DeviceStatus
}

/** Browser-only chart point; temperature_time preserves the real sensor read time. */
export interface Point {
  time: number
  cpu: number
  memory: number
  rx: number
  tx: number
  temperature: number | null
  temperature_time: number | null
}

/** Server-approved metric schema used by the explicit historical query form. */
export interface MetricDescriptor {
  id: string
  name: string
  unit: string
  dimensions?: string[]
  description: string
}

/** One persisted metric observation; absent values remain gaps rather than zeroes. */
export interface HistoricalPoint {
  device_id: string
  descriptor_id: string
  sampled_at: string
  value?: number
  dimensions?: Record<string, string>
  quality: 'valid' | 'partial' | 'estimated' | 'missing' | 'reset' | 'stale'
}

/** Bounded history response returned by the authenticated Server query API. */
export interface HistoricalMetricResult {
  descriptor: MetricDescriptor
  device_id: string
  from: string
  to: string
  points: HistoricalPoint[]
}

/** Metric schema reconstructed from validated observations for one exact plugin version. */
export interface PluginMetricDescriptor {
  device_id: string
  plugin_id: string
  plugin_version: string
  collector_id: string
  metric_id: string
  type: 'gauge' | 'counter' | 'state' | 'event'
  unit: string
  label_keys: string[]
  first_observed_at: string
  last_observed_at: string
}

export interface PluginHistoricalPoint {
  observed_at: string
  type: PluginMetricDescriptor['type']
  unit: string
  value?: unknown
  summary?: Record<string, unknown>
  quality: HistoricalPoint['quality']
  labels?: Record<string, string>
  resolution_seconds: number
  aggregation_kind: 'raw' | 'gauge_summary' | 'counter_delta' | 'state_summary' | 'event_count'
}

export interface PluginHistoricalMetricResult {
  device_id: string
  plugin_id: string
  plugin_version: string
  collector_id: string
  metric_id: string
  from: string
  to: string
  points: PluginHistoricalPoint[]
}

/** Lifecycle state for one explicitly authorized disk-capacity refresh. */
export interface RefreshJob {
  request_id: string
  device_id: string
  status: 'queued' | 'delivered' | 'completed' | 'failed' | 'expired'
  error?: string
}
