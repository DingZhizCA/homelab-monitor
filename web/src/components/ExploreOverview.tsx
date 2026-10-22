/**
 * 展示浏览器短时跨设备比较，并承载显式提交的 Server 历史查询。
 * 两种视图都只读现有数据，不触发设备采集。
 */
import { lazy, Suspense, useMemo, useState } from 'react'
import type { EChartsOption } from 'echarts'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { HistoricalMetricExplorer } from '@/components/HistoricalMetricExplorer'
import { Skeleton } from '@/components/ui/skeleton'
import { chartTooltip } from '@/lib/chart'
import { comparisonSeries, exploreMetricOptions, type ExploreMetric } from '@/lib/explore'
import { formatByteRate, formatPercent } from '@/lib/metrics'
import type { DeviceState, Point } from '@/types'

const MetricChart = lazy(() => import('@/components/MetricChart').then(module => ({ default: module.MetricChart })))

export function ExploreOverview({ devices, history, dark }: { devices: DeviceState[]; history: Record<string, Point[]>; dark: boolean }) {
  const [metric, setMetric] = useState<ExploreMetric>('cpu')
  const series = useMemo(() => comparisonSeries(devices, history, metric), [devices, history, metric])
  const selected = exploreMetricOptions.find(option => option.id === metric)!
  const byteMetric = metric === 'rx' || metric === 'tx'
  const formatTime = (value: number) => new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(new Date(value))
  const option: EChartsOption = {
    animation: false,
    tooltip: {
      trigger: 'axis', confine: true, transitionDuration: 0,
      formatter: params => chartTooltip(params, byteMetric ? 'network' : 'resource', formatTime),
    },
    legend: { top: 0, type: 'scroll', textStyle: { color: dark ? '#cbd5e1' : '#475569' } },
    grid: { left: 8, right: 10, top: 54, bottom: 12, containLabel: true },
    xAxis: { type: 'time', axisLabel: { hideOverlap: true, color: dark ? '#94a3b8' : '#64748b' } },
    yAxis: {
      type: 'value', min: byteMetric ? undefined : 0, max: byteMetric ? undefined : 100,
      axisLabel: { formatter: (value: number) => byteMetric ? formatByteRate(value) : formatPercent(value), color: dark ? '#94a3b8' : '#64748b' },
    },
    series,
  }

  return <section aria-labelledby="explore-title" className="min-w-0">
    <div className="mb-5"><h2 id="explore-title" className="text-2xl font-semibold">临时指标对比</h2><p className="mt-1 text-sm text-muted-foreground">仅使用本页面打开期间浏览器保留的最近样本；刷新页面后历史会清空，不会触发额外采集。</p></div>
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-3"><CardTitle className="text-base">{selected.label}</CardTitle>
        <label className="flex items-center gap-2 text-sm">比较指标<select aria-label="比较指标" value={metric} onChange={event => setMetric(event.target.value as ExploreMetric)} className="rounded-md border bg-background px-2 py-1.5">
          {exploreMetricOptions.map(item => <option key={item.id} value={item.id}>{item.label}</option>)}
        </select></label>
      </CardHeader>
      <CardContent>{series.length === 0 ? <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">还没有可比较的样本。等待现有 Agent 上报即可。</p> : <Suspense fallback={<Skeleton className="h-56 sm:h-64"/>}><MetricChart option={option} label={`${selected.label}跨设备比较`}/></Suspense>}</CardContent>
    </Card>
    <HistoricalMetricExplorer devices={devices} dark={dark}/>
  </section>
}
