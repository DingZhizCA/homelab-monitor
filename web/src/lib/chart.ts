/**
 * Dashboard 趋势图的纯数据变换。
 * 负责温度时间去重、坐标范围和 tooltip 文本，不创建 ECharts 实例。
 */
import { formatByteRate, formatPercent } from '@/lib/metrics'
import type { Point } from '@/types'

export type ChartMode = 'resource' | 'network' | 'temperature'
type TooltipRow = { axisValue?: unknown; seriesName?: unknown; value?: unknown }

/** Build unique temperature points and a padded domain that includes sensor time. */
export function temperatureChartData(points: Point[]) {
  const data = [...new Map(points.filter(point => point.temperature != null && point.temperature_time != null).map(point => [point.temperature_time!, [point.temperature_time!, point.temperature!] as [number, number]])).values()]
  const domainTimes = [...points.map(point => point.time), ...data.map(row => row[0])]
  if (!domainTimes.length) return { data, min: undefined, max: undefined }
  const domainMin = Math.min(...domainTimes), domainMax = Math.max(...domainTimes)
  const padding = Math.max(5_000, (domainMax - domainMin) * 0.04)
  return { data, min: domainMin - padding, max: domainMax + padding }
}

/** Format an axis tooltip with explicit series names, units, and bounded precision. */
export function chartTooltip(params: unknown, mode: ChartMode, formatTime: (value: number) => string) {
  const rows = (Array.isArray(params) ? params : [params]).filter((row): row is TooltipRow => typeof row === 'object' && row !== null)
  if (!rows.length) return ''
  const axisValue = Number(rows[0].axisValue)
  const lines = Number.isFinite(axisValue) ? [formatTime(axisValue)] : []
  for (const row of rows) {
    const raw = Array.isArray(row.value) ? row.value[1] : row.value
    const value = Number(raw)
    if (!Number.isFinite(value)) continue
    const formatted = mode === 'network' ? formatByteRate(value) : mode === 'temperature' ? `${value.toFixed(1)} °C` : formatPercent(value)
    lines.push(`${escapeTooltipText(String(row.seriesName || '数值'))}：${formatted}`)
  }
  return lines.join('<br/>')
}

function escapeTooltipText(value: string) {
  return value.replace(/[&<>"']/g, character => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  })[character]!)
}
