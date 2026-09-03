import { describe, expect, it } from 'vitest'
import { diskPowerSummary, formatByteRate, formatBytes, formatPercent } from './metrics'
import type { DiskMetric } from '@/types'

const disk = (power_state?: 'active' | 'standby' | 'sleeping' | 'unknown'): DiskMetric => ({
  device: '/dev/sdb1', mount_point: '/data', filesystem: 'ext4', media_type: 'hdd',
  physical_devices: [{ name: 'sdb', rotational: true, power_state, power_state_collected_at: power_state ? '2026-09-03T12:00:00Z' : undefined }],
  capacity_available: false, total_bytes: 0, used_bytes: 0, available_bytes: 0,
  usage_percent: 0, collected_at: '0001-01-01T00:00:00Z', collection_mode: '',
})

describe('metric formatting', () => {
  it('uses bounded precision and adaptive units', () => {
    expect(formatPercent(9.1267)).toBe('9.1%')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatByteRate(2 * 1024 * 1024)).toBe('2.0 MB/s')
  })

  it('keeps unknown and missing HDD power states distinct', () => {
    expect(diskPowerSummary(disk('standby')).label).toBe('已休眠')
    expect(diskPowerSummary(disk('unknown')).label).toBe('未知')
    expect(diskPowerSummary(disk()).label).toBe('未启用或不支持')
  })
})
