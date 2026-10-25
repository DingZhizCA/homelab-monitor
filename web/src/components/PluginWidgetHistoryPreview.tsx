/** Explicit, bounded plugin history preview for one persisted Dashboard Widget. */
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import type { EChartsOption } from 'echarts'
import { Skeleton } from '@/components/ui/skeleton'
import { pluginMetricLabel, pluginNumericPoint, pluginPointDetail, pluginQualityLabel } from '@/lib/pluginMetrics'
import type { PluginHistoricalMetricResult, PluginMetricReference } from '@/types'

const MetricChart = lazy(() => import('@/components/MetricChart').then(module => ({ default: module.MetricChart })))

export function PluginWidgetHistoryPreview({ widgetID, deviceID, reference, dark }: { widgetID: string; deviceID: string; reference: PluginMetricReference; dark: boolean }) {
  const [from, setFrom] = useState(''), [to, setTo] = useState(''), [limit, setLimit] = useState('')
  const [result, setResult] = useState<PluginHistoricalMetricResult | null>(null)
  const [error, setError] = useState(''), [loading, setLoading] = useState(false)
  const requestController = useRef<AbortController | null>(null)
  useEffect(() => () => requestController.current?.abort(), [])
  const option = useMemo<EChartsOption | null>(() => {
    if (!result) return null
    const grouped = new Map<string, { name: string; data: [number, number | null][] }>()
    for (const point of result.points) {
      const labels = Object.entries(point.labels || {}).sort(([left], [right]) => left.localeCompare(right))
      const name = labels.length ? labels.map(([key, value]) => `${key}=${value}`).join(', ') : result.metric_id
      const series = grouped.get(name) || { name, data: [] }
      series.data.push([new Date(point.observed_at).getTime(), pluginNumericPoint(point)])
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

  async function query(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(''); setResult(null)
    const start = new Date(from), end = new Date(to), pointLimit = Number(limit)
    if (!from || !to || !Number.isInteger(pointLimit) || pointLimit < 1 || !Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || start >= end) {
      setError('请输入有效的起止时间和正整数点数上限。'); return
    }
    const params = new URLSearchParams({ device_id: deviceID, ...reference, from: start.toISOString(), to: end.toISOString(), limit: String(pointLimit) })
    const controller = new AbortController(); requestController.current?.abort(); requestController.current = controller; setLoading(true)
    try {
      const response = await fetch(`/api/v1/dashboard/plugin-metrics/history?${params}`, { credentials: 'same-origin', signal: controller.signal })
      if (!response.ok) throw new Error(response.status === 404 ? 'Server 尚未启用插件历史查询。' : response.status === 422 ? '查询超过 Server 的插件观测扫描预算。' : `插件历史查询失败（${response.status}）。`)
      setResult(await response.json() as PluginHistoricalMetricResult)
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === 'AbortError')) setError(reason instanceof Error ? reason.message : '插件历史查询失败。')
    } finally {
      if (requestController.current === controller) { requestController.current = null; if (!controller.signal.aborted) setLoading(false) }
    }
  }

  const resolutions = result ? [...new Set(result.points.map(point => point.resolution_seconds === 0 ? '原始' : `${point.resolution_seconds} 秒`))] : []
  return <section aria-label={`${pluginMetricLabel(reference)} 历史预览`} className="mt-3 rounded-md bg-muted/30 p-3">
    <form onSubmit={event => { void query(event) }} className="grid gap-2 sm:grid-cols-4">
      <label className="grid gap-1 text-xs">开始时间<input aria-label={`${widgetID} 插件历史开始时间`} type="datetime-local" value={from} onChange={event => setFrom(event.target.value)} className="rounded-md border bg-background px-2 py-1.5"/></label>
      <label className="grid gap-1 text-xs">结束时间<input aria-label={`${widgetID} 插件历史结束时间`} type="datetime-local" value={to} onChange={event => setTo(event.target.value)} className="rounded-md border bg-background px-2 py-1.5"/></label>
      <label className="grid gap-1 text-xs">最大点数<input aria-label={`${widgetID} 插件历史最大点数`} type="number" min="1" step="1" value={limit} onChange={event => setLimit(event.target.value)} className="rounded-md border bg-background px-2 py-1.5"/></label>
      <div className="flex items-end"><button type="submit" disabled={loading} className="rounded-md border px-3 py-1.5 text-xs disabled:opacity-50">{loading ? '查询中…' : '查询插件历史'}</button></div>
    </form>
    {error && <p role="alert" className="mt-2 text-xs text-destructive">{error}</p>}
    {result && <div className="mt-3">{result.points.length === 0 ? <p role="status" className="text-xs text-muted-foreground">所选范围内没有插件历史点。</p> : <>
      <p className="mb-2 text-xs text-muted-foreground">分辨率：{resolutions.join('、')} · {result.points.map(point => pluginQualityLabel[point.quality]).filter((value, index, all) => all.indexOf(value) === index).join('、')}</p>
      {option ? <Suspense fallback={<Skeleton className="h-48"/>}><MetricChart option={option} label={`${pluginMetricLabel(reference)} Dashboard 历史趋势`}/></Suspense> : <p className="text-xs text-muted-foreground">当前结果没有可绘制的数值；请查看类型化明细。</p>}
      <ol className="mt-2 max-h-48 space-y-1 overflow-auto text-xs">{result.points.map((point, index) => <li key={`${point.observed_at}-${index}`}><time>{new Date(point.observed_at).toLocaleString()}</time> · {point.aggregation_kind} · {point.resolution_seconds === 0 ? '原始' : `${point.resolution_seconds} 秒`} · {pluginQualityLabel[point.quality]} · {pluginPointDetail(point)}</li>)}</ol>
    </>}</div>}
  </section>
}
