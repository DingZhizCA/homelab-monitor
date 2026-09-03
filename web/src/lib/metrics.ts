/**
 * Dashboard 指标格式化与磁盘展示派生逻辑。
 * 这些函数保持无副作用，使单位、精度和缺失值语义可以独立测试。
 */
import type { DiskMetric } from '@/types'

/** Format a 0–100 measurement with one decimal place. */
export const formatPercent = (value: number) => `${value.toFixed(1)}%`

/** Format bytes using binary thresholds and at most one decimal place. */
export function formatBytes(value: number) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

/** Format a bytes-per-second rate with the same adaptive unit policy. */
export const formatByteRate = (value: number) => `${formatBytes(value)}/s`

const powerLabels = { active: '运行中', standby: '已休眠', sleeping: '深度休眠', unknown: '未知' }

/** Summarize physical HDD states without treating missing data as a known state. */
export function diskPowerSummary(disk: DiskMetric) {
  const devices = disk.physical_devices?.filter(device => device.rotational) || []
  if (!devices.length) return { label: '不适用', checkedAt: null, state: 'not_applicable' as const }
  const states = devices.map(device => device.power_state)
  const label = states.map(state => state ? powerLabels[state] : '未启用或不支持').join(', ')
  const checkedAt = devices.find(device => device.power_state_collected_at)?.power_state_collected_at || null
  const state = states.every(value => value === 'standby' || value === 'sleeping') ? 'standby' : states.some(value => value === 'active') ? 'active' : 'unknown'
  return { label, checkedAt, state }
}
