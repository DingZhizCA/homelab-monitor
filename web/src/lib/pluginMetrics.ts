/** Shared formatting and chart semantics for typed plugin metric history. */
import type { PluginHistoricalPoint, PluginMetricDescriptor, PluginMetricReference } from '@/types'

export const pluginQualityLabel: Record<PluginHistoricalPoint['quality'], string> = {
  valid: '有效', partial: '部分', estimated: '估算', missing: '缺失', reset: '计数重置', stale: '过期',
}

export function pluginMetricKey(reference: PluginMetricReference) {
  return [reference.plugin_id, reference.plugin_version, reference.collector_id, reference.metric_id].join('\u0000')
}

export function pluginMetricLabel(reference: PluginMetricReference) {
  return `${reference.plugin_id}@${reference.plugin_version} · ${reference.collector_id}/${reference.metric_id}`
}

export function pluginNumericPoint(point: PluginHistoricalPoint): number | null {
  if (point.aggregation_kind === 'raw') return typeof point.value === 'number' && Number.isFinite(point.value) ? point.value : null
  const key = point.aggregation_kind === 'gauge_summary' ? 'mean' : point.aggregation_kind === 'counter_delta' ? 'delta' : point.aggregation_kind === 'event_count' ? 'event_count' : ''
  const value = key ? point.summary?.[key] : undefined
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

export function pluginPointDetail(point: PluginHistoricalPoint) {
  if (point.aggregation_kind === 'raw') return typeof point.value === 'string' ? point.value : JSON.stringify(point.value)
  const summary = point.summary || {}
  if (point.aggregation_kind === 'gauge_summary') return `均值 ${summary.mean}，最小 ${summary.min}，最大 ${summary.max}，最后 ${summary.last}`
  if (point.aggregation_kind === 'counter_delta') return `增量 ${summary.delta}，重置 ${summary.reset_count} 次，首值 ${summary.first}，末值 ${summary.last}`
  if (point.aggregation_kind === 'state_summary') return `最后状态 ${JSON.stringify(summary.last)}，转换 ${summary.transition_count} 次`
  return `事件 ${summary.event_count} 次`
}

export function descriptorReference(descriptor: PluginMetricDescriptor): PluginMetricReference {
  return { plugin_id: descriptor.plugin_id, plugin_version: descriptor.plugin_version, collector_id: descriptor.collector_id, metric_id: descriptor.metric_id }
}
