/**
 * Authenticated, explicit historical metric query UI.
 * It only reads the optional Server history API and never schedules collection.
 */
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import type { EChartsOption } from 'echarts'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { formatByteRate, formatPercent } from '@/lib/metrics'
import type { DeviceState, HistoricalMetricResult, MetricDescriptor } from '@/types'

const MetricChart = lazy(() => import('@/components/MetricChart').then(module => ({ default: module.MetricChart })))
const qualityLabels: Record<HistoricalMetricResult['points'][number]['quality'], string> = {
  valid: '有效', partial: '部分', estimated: '估算', missing: '缺失', reset: '计数重置', stale: '过期',
}

export function HistoricalMetricExplorer({ devices, dark }: { devices: DeviceState[]; dark: boolean }) {
  const [descriptors, setDescriptors] = useState<MetricDescriptor[]>([])
  const [descriptorMessage, setDescriptorMessage] = useState('正在读取可查询指标…')
  const [deviceID, setDeviceID] = useState('')
  const [descriptorID, setDescriptorID] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [limit, setLimit] = useState('')
  const [result, setResult] = useState<HistoricalMetricResult | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const historyRequest = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/dashboard/metrics/descriptors', { credentials: 'same-origin', signal: controller.signal })
      .then(async response => {
        if (!response.ok) throw new Error(response.status === 404 ? 'Server 尚未启用指标目录接口。' : `指标目录请求失败（${response.status}）。`)
        const payload = await response.json() as { descriptors?: MetricDescriptor[] }
        if (!Array.isArray(payload.descriptors)) throw new Error('Server 返回的指标目录格式无效。')
        setDescriptors(payload.descriptors)
        setDescriptorMessage(payload.descriptors.length === 0 ? '当前没有可查询指标。' : '')
        setDescriptorID(current => current || payload.descriptors?.[0]?.id || '')
      })
      .catch(reason => {
        if (reason instanceof Error && reason.name === 'AbortError') return
        setDescriptorMessage(reason instanceof Error ? reason.message : '无法读取指标目录。')
      })
    return () => {
      controller.abort()
      historyRequest.current?.abort()
    }
  }, [])

  const selectedDescriptor = descriptors.find(item => item.id === descriptorID)
  const option = useMemo<EChartsOption | null>(() => {
    if (!result) return null
    const grouped = new Map<string, { name: string; data: [number, number | null][] }>()
    for (const point of result.points) {
      const dimensions = Object.entries(point.dimensions || {}).sort(([left], [right]) => left.localeCompare(right))
      const name = dimensions.length ? dimensions.map(([key, value]) => `${key}=${value}`).join(', ') : result.descriptor.name
      const series = grouped.get(name) || { name, data: [] }
      series.data.push([new Date(point.sampled_at).getTime(), point.value ?? null])
      grouped.set(name, series)
    }
    const percent = result.descriptor.unit === 'percent'
    const byteRate = result.descriptor.unit === 'bytes_per_second'
    return {
      animation: false,
      tooltip: { trigger: 'axis', confine: true, transitionDuration: 0 },
      legend: { top: 0, type: 'scroll', textStyle: { color: dark ? '#cbd5e1' : '#475569' } },
      grid: { left: 8, right: 10, top: 54, bottom: 12, containLabel: true },
      xAxis: { type: 'time', axisLabel: { hideOverlap: true, color: dark ? '#94a3b8' : '#64748b' } },
      yAxis: {
        type: 'value', min: percent ? 0 : undefined, max: percent ? 100 : undefined,
        axisLabel: {
          formatter: (value: number) => percent ? formatPercent(value) : byteRate ? formatByteRate(value) : `${value}${result.descriptor.unit === 'celsius' ? ' °C' : ''}`,
          color: dark ? '#94a3b8' : '#64748b',
        },
      },
      series: [...grouped.values()].map(series => ({ type: 'line', showSymbol: false, connectNulls: false, ...series })),
    }
  }, [dark, result])

  async function query(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setResult(null)
    const start = new Date(from)
    const end = new Date(to)
    const pointLimit = Number(limit)
    if (!deviceID || !descriptorID || !from || !to || !Number.isInteger(pointLimit) || pointLimit < 1 || !Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || start >= end) {
      setError('请选择设备、指标、有效的起止时间和正整数点数上限。')
      return
    }
    const params = new URLSearchParams({
      descriptor_id: descriptorID,
      device_id: deviceID,
      from: start.toISOString(),
      to: end.toISOString(),
      limit: String(pointLimit),
    })
    const controller = new AbortController()
    historyRequest.current = controller
    setLoading(true)
    try {
      const response = await fetch(`/api/v1/dashboard/metrics/history?${params}`, { credentials: 'same-origin', signal: controller.signal })
      if (!response.ok) throw new Error(response.status === 404 ? 'Server 尚未启用历史查询接口或历史存储。' : response.status === 401 ? '登录会话已失效，请重新登录。' : `历史查询失败（${response.status}）。`)
      setResult(await response.json() as HistoricalMetricResult)
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === 'AbortError')) setError(reason instanceof Error ? reason.message : '历史查询失败。')
    } finally {
      if (historyRequest.current === controller) {
        historyRequest.current = null
        if (!controller.signal.aborted) setLoading(false)
      }
    }
  }

  const qualitySummary = result?.points.reduce<Record<string, number>>((counts, point) => {
    counts[point.quality] = (counts[point.quality] || 0) + 1
    return counts
  }, {})
  const latestSample = result?.points.reduce<string | null>((latest, point) => !latest || point.sampled_at > latest ? point.sampled_at : latest, null)
  const displayTime = (value: string) => new Intl.DateTimeFormat(undefined, {
    year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short',
  }).format(new Date(value))

  return <section aria-labelledby="history-title" className="mt-6 min-w-0">
    <div className="mb-4"><h3 id="history-title" className="text-xl font-semibold">持久化历史查询</h3><p className="mt-1 text-sm text-muted-foreground">仅查询 Server 已保存的数据，不会触发设备采集。历史接口和存储源需由 Server 显式配置。</p></div>
    <Card>
      <CardHeader><CardTitle className="text-base">选择查询范围</CardTitle></CardHeader>
      <CardContent>
        <form onSubmit={event => { void query(event) }} className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <label className="grid gap-1 text-sm">设备<select required aria-label="历史设备" value={deviceID} onChange={event => { setDeviceID(event.target.value); setResult(null) }} className="rounded-md border bg-background px-2 py-2"><option value="">请选择设备</option>{devices.map(device => <option key={device.snapshot.device.id} value={device.snapshot.device.id}>{device.snapshot.device.id}</option>)}</select></label>
          <label className="grid gap-1 text-sm">指标<select required aria-label="历史指标" value={descriptorID} onChange={event => { setDescriptorID(event.target.value); setResult(null) }} disabled={!descriptors.length} className="rounded-md border bg-background px-2 py-2"><option value="">请选择指标</option>{descriptors.map(descriptor => <option key={descriptor.id} value={descriptor.id}>{descriptor.name}</option>)}</select></label>
          <label className="grid gap-1 text-sm">开始时间<input required aria-label="开始时间" type="datetime-local" value={from} onChange={event => { setFrom(event.target.value); setResult(null) }} className="rounded-md border bg-background px-2 py-2"/></label>
          <label className="grid gap-1 text-sm">结束时间<input required aria-label="结束时间" type="datetime-local" value={to} onChange={event => { setTo(event.target.value); setResult(null) }} className="rounded-md border bg-background px-2 py-2"/></label>
          <label className="grid gap-1 text-sm">最大点数<input required aria-label="最大点数" type="number" min="1" step="1" value={limit} onChange={event => { setLimit(event.target.value); setResult(null) }} className="rounded-md border bg-background px-2 py-2"/></label>
          <div className="flex items-end sm:col-span-2 xl:col-span-5"><button type="submit" disabled={loading || !descriptors.length} className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white disabled:cursor-not-allowed disabled:opacity-50">{loading ? '查询中…' : '查询历史'}</button></div>
        </form>
        {descriptorMessage && <p role="status" className="mt-3 text-sm text-muted-foreground">{descriptorMessage}</p>}
        {error && <p role="alert" className="mt-3 text-sm text-destructive">{error}</p>}
        {result && <div className="mt-5">
          {result.points.length === 0 ? <p role="status" className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">所选范围内没有历史点。</p> : <>
            <p className="mb-3 text-xs text-muted-foreground">来源：Server 历史源 · 时区：{Intl.DateTimeFormat().resolvedOptions().timeZone} · {result.device_id} · {displayTime(result.from)} – {displayTime(result.to)} · 最近样本：{latestSample ? displayTime(latestSample) : '暂无'} · {result.points.length} 点 · 聚合粒度由历史源决定（接口未返回粒度元数据） · {Object.entries(qualitySummary || {}).map(([quality, count]) => `${qualityLabels[quality as keyof typeof qualityLabels] || quality} ${count}`).join('，')}；缺失值显示为图表断点。</p>
            {option && <Suspense fallback={<Skeleton className="h-56 sm:h-64"/>}><MetricChart option={option} label={`${result.descriptor.name}历史趋势`}/></Suspense>}
          </>}
        </div>}
        {selectedDescriptor?.description && <p className="mt-3 text-xs text-muted-foreground">{selectedDescriptor.description}</p>}
      </CardContent>
    </Card>
  </section>
}
