/**
 * Explicit plugin history explorer backed by the observed Descriptor Catalog.
 * Catalog and history reads never start collection or contact plugin processes.
 */
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import type { EChartsOption } from 'echarts'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import type { DeviceState, PluginHistoricalMetricResult, PluginHistoricalPoint, PluginMetricDescriptor } from '@/types'

const MetricChart = lazy(() => import('@/components/MetricChart').then(module => ({ default: module.MetricChart })))

const qualityLabel: Record<PluginHistoricalPoint['quality'], string> = {
  valid: '有效', partial: '部分', estimated: '估算', missing: '缺失', reset: '计数重置', stale: '过期',
}

function descriptorKey(item: PluginMetricDescriptor) {
  return [item.plugin_id, item.plugin_version, item.collector_id, item.metric_id].join('\u0000')
}

function numericPoint(point: PluginHistoricalPoint): number | null {
  if (point.aggregation_kind === 'raw') return typeof point.value === 'number' && Number.isFinite(point.value) ? point.value : null
  const key = point.aggregation_kind === 'gauge_summary' ? 'mean' : point.aggregation_kind === 'counter_delta' ? 'delta' : point.aggregation_kind === 'event_count' ? 'event_count' : ''
  const value = key ? point.summary?.[key] : undefined
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function pointDetail(point: PluginHistoricalPoint) {
  if (point.aggregation_kind === 'raw') return typeof point.value === 'string' ? point.value : JSON.stringify(point.value)
  const summary = point.summary || {}
  if (point.aggregation_kind === 'gauge_summary') return `均值 ${summary.mean}，最小 ${summary.min}，最大 ${summary.max}，最后 ${summary.last}`
  if (point.aggregation_kind === 'counter_delta') return `增量 ${summary.delta}，重置 ${summary.reset_count} 次，首值 ${summary.first}，末值 ${summary.last}`
  if (point.aggregation_kind === 'state_summary') return `最后状态 ${JSON.stringify(summary.last)}，转换 ${summary.transition_count} 次`
  return `事件 ${summary.event_count} 次`
}

export function PluginHistoricalMetricExplorer({ devices, dark }: { devices: DeviceState[]; dark: boolean }) {
  const [deviceID, setDeviceID] = useState(devices[0]?.snapshot.device.id || '')
  const [descriptors, setDescriptors] = useState<PluginMetricDescriptor[]>([])
  const [descriptor, setDescriptor] = useState('')
  const [catalogMessage, setCatalogMessage] = useState(deviceID ? '正在读取已观测插件指标…' : '请选择设备。')
  const [from, setFrom] = useState(''), [to, setTo] = useState(''), [limit, setLimit] = useState('')
  const [result, setResult] = useState<PluginHistoricalMetricResult | null>(null)
  const [error, setError] = useState(''), [loading, setLoading] = useState(false)
  const historyRequest = useRef<AbortController | null>(null)
  const activeDeviceID = devices.some(device => device.snapshot.device.id === deviceID) ? deviceID : devices[0]?.snapshot.device.id || ''

  useEffect(() => {
    if (!activeDeviceID) return
    const controller = new AbortController()
    const query = new URLSearchParams({ device_id: activeDeviceID })
    fetch(`/api/v1/dashboard/plugin-metrics/descriptors?${query}`, { credentials: 'same-origin', signal: controller.signal })
      .then(async response => {
        if (!response.ok) throw new Error(response.status === 404 ? 'Server 尚未启用插件指标目录。' : `插件指标目录请求失败（${response.status}）。`)
        const payload = await response.json() as { descriptors?: PluginMetricDescriptor[] }
        if (!Array.isArray(payload.descriptors)) throw new Error('Server 返回的插件指标目录格式无效。')
        setDescriptors(payload.descriptors)
        setDescriptor(payload.descriptors[0] ? descriptorKey(payload.descriptors[0]) : '')
        setCatalogMessage(payload.descriptors.length ? '' : '该设备尚无成功观测的插件指标。')
      })
      .catch(reason => {
        if (!(reason instanceof Error && reason.name === 'AbortError')) setCatalogMessage(reason instanceof Error ? reason.message : '无法读取插件指标目录。')
      })
    return () => controller.abort()
  }, [activeDeviceID])

  useEffect(() => () => historyRequest.current?.abort(), [])
  const selected = descriptors.find(item => descriptorKey(item) === descriptor)
  const option = useMemo<EChartsOption | null>(() => {
    if (!result) return null
    const grouped = new Map<string, { name: string; data: [number, number | null][] }>()
    for (const point of result.points) {
      const labels = Object.entries(point.labels || {}).sort(([a], [b]) => a.localeCompare(b))
      const name = labels.length ? labels.map(([key, value]) => `${key}=${value}`).join(', ') : result.metric_id
      const series = grouped.get(name) || { name, data: [] }
      series.data.push([new Date(point.observed_at).getTime(), numericPoint(point)])
      grouped.set(name, series)
    }
    if (![...grouped.values()].some(series => series.data.some(([, value]) => value != null))) return null
    return {
      animation: false,
      tooltip: { trigger: 'axis', confine: true, transitionDuration: 0 },
      legend: { top: 0, type: 'scroll', textStyle: { color: dark ? '#cbd5e1' : '#475569' } },
      grid: { left: 8, right: 10, top: 54, bottom: 12, containLabel: true },
      xAxis: { type: 'time', axisLabel: { hideOverlap: true, color: dark ? '#94a3b8' : '#64748b' } },
      yAxis: { type: 'value', axisLabel: { color: dark ? '#94a3b8' : '#64748b' } },
      series: [...grouped.values()].map(series => ({ type: 'line', showSymbol: false, connectNulls: false, ...series })),
    }
  }, [dark, result])

  async function queryHistory(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(''); setResult(null)
    const start = new Date(from), end = new Date(to), pointLimit = Number(limit)
    if (!selected || !from || !to || !Number.isInteger(pointLimit) || pointLimit < 1 || !Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || start >= end) { setError('请选择插件指标、有效的起止时间和正整数点数上限。'); return }
    const params = new URLSearchParams({ device_id: selected.device_id, plugin_id: selected.plugin_id, plugin_version: selected.plugin_version, collector_id: selected.collector_id, metric_id: selected.metric_id, from: start.toISOString(), to: end.toISOString(), limit: String(pointLimit) })
    const controller = new AbortController(); historyRequest.current?.abort(); historyRequest.current = controller; setLoading(true)
    try {
      const response = await fetch(`/api/v1/dashboard/plugin-metrics/history?${params}`, { credentials: 'same-origin', signal: controller.signal })
      if (!response.ok) throw new Error(response.status === 404 ? 'Server 尚未启用插件历史查询。' : response.status === 422 ? '查询超过 Server 的插件观测扫描预算。' : `插件历史查询失败（${response.status}）。`)
      setResult(await response.json() as PluginHistoricalMetricResult)
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === 'AbortError')) setError(reason instanceof Error ? reason.message : '插件历史查询失败。')
    } finally {
      if (historyRequest.current === controller) { historyRequest.current = null; if (!controller.signal.aborted) setLoading(false) }
    }
  }

  const resolutions = result ? [...new Set(result.points.map(point => point.resolution_seconds === 0 ? '原始' : `${point.resolution_seconds} 秒`))] : []
  return <section aria-labelledby="plugin-history-title" className="mt-6 min-w-0">
    <div className="mb-4"><h3 id="plugin-history-title" className="text-xl font-semibold">插件指标历史</h3><p className="mt-1 text-sm text-muted-foreground">目录只包含 Server 已成功接收的数据，读取不会启动 Collector。</p></div>
    <Card><CardHeader><CardTitle className="text-base">选择版本化插件指标</CardTitle></CardHeader><CardContent>
      <form onSubmit={event => { void queryHistory(event) }} className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <label className="grid gap-1 text-sm">设备<select aria-label="插件历史设备" value={activeDeviceID} onChange={event => { const value = event.target.value; setDeviceID(value); setResult(null); setDescriptors([]); setDescriptor(''); setCatalogMessage(value ? '正在读取已观测插件指标…' : '请选择设备。') }} className="rounded-md border bg-background px-2 py-2"><option value="">请选择设备</option>{devices.map(device => <option key={device.snapshot.device.id} value={device.snapshot.device.id}>{device.snapshot.device.id}</option>)}</select></label>
        <label className="grid gap-1 text-sm">插件指标<select aria-label="插件历史指标" value={descriptor} onChange={event => { setDescriptor(event.target.value); setResult(null) }} disabled={!descriptors.length} className="rounded-md border bg-background px-2 py-2"><option value="">请选择指标</option>{descriptors.map(item => <option key={descriptorKey(item)} value={descriptorKey(item)}>{item.plugin_id}@{item.plugin_version} · {item.collector_id}/{item.metric_id}</option>)}</select></label>
        <label className="grid gap-1 text-sm">开始时间<input aria-label="插件开始时间" type="datetime-local" value={from} onChange={event => setFrom(event.target.value)} className="rounded-md border bg-background px-2 py-2"/></label>
        <label className="grid gap-1 text-sm">结束时间<input aria-label="插件结束时间" type="datetime-local" value={to} onChange={event => setTo(event.target.value)} className="rounded-md border bg-background px-2 py-2"/></label>
        <label className="grid gap-1 text-sm">最大点数<input aria-label="插件最大点数" type="number" min="1" step="1" value={limit} onChange={event => setLimit(event.target.value)} className="rounded-md border bg-background px-2 py-2"/></label>
        <div className="flex items-end sm:col-span-2 xl:col-span-5"><button type="submit" disabled={loading || !selected} className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white disabled:opacity-50">{loading ? '查询中…' : '查询插件历史'}</button></div>
      </form>
      {catalogMessage && <p role="status" className="mt-3 text-sm text-muted-foreground">{catalogMessage}</p>}{error && <p role="alert" className="mt-3 text-sm text-destructive">{error}</p>}
      {selected && <p className="mt-3 text-xs text-muted-foreground">类型：{selected.type} · 单位：{selected.unit} · 标签：{selected.label_keys.join(', ') || '无'} · 首次观测：{new Date(selected.first_observed_at).toLocaleString()} · 最近观测：{new Date(selected.last_observed_at).toLocaleString()}</p>}
      {result && <div className="mt-5">{result.points.length === 0 ? <p role="status" className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">所选范围内没有插件历史点。</p> : <>
        <p className="mb-3 text-xs text-muted-foreground">分辨率：{resolutions.join('、')} · {result.points.map(point => qualityLabel[point.quality]).filter((value, index, all) => all.indexOf(value) === index).join('、')}。State 摘要和非数值原始值保留在明细中。</p>
        {option ? <Suspense fallback={<Skeleton className="h-56 sm:h-64"/>}><MetricChart option={option} label={`${result.plugin_id} ${result.metric_id}插件历史趋势`}/></Suspense> : <p className="rounded-md bg-muted p-3 text-sm text-muted-foreground">当前结果没有可绘制的数值；请查看下方类型化明细。</p>}
        <ol className="mt-3 max-h-64 space-y-1 overflow-auto text-xs">{result.points.map((point, index) => <li key={`${point.observed_at}-${index}`} className="rounded border p-2"><time>{new Date(point.observed_at).toLocaleString()}</time> · {point.aggregation_kind} · {point.resolution_seconds === 0 ? '原始' : `${point.resolution_seconds} 秒`} · {qualityLabel[point.quality]} · {pointDetail(point)}</li>)}</ol>
      </>}</div>}
    </CardContent></Card>
  </section>
}
