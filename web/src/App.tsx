/**
 * Dashboard 页面编排层。
 * 负责把 REST/SSE 设备状态映射为摘要、临时趋势、文件系统和网络视图；
 * 不采集指标，也不提供 Server 端历史持久化。
 * 数据链路：Server API -> browser history buffer -> cards and ECharts
 */
import { lazy, Suspense, useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Activity, Clock3, Cpu, Gauge, HardDrive, LogOut, MemoryStick, Moon, RefreshCw, Server, Sun, Thermometer, Wifi } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { AppNavigation, type AppPage } from '@/components/AppNavigation'
import { AlertsOverview } from '@/components/AlertsOverview'
import { DashboardManagement } from '@/components/DashboardManagement'
import { ExploreOverview } from '@/components/ExploreOverview'
import { LoginScreen } from '@/components/LoginScreen'
import { NetworkOverview } from '@/components/NetworkOverview'
import { OwnerUserManagement } from '@/components/OwnerUserManagement'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import type { DeviceState, DiskMetric, Point, RefreshJob } from '@/types'

import { chartTooltip, temperatureChartData, type ChartMode } from '@/lib/chart'
import { appendPoint } from '@/lib/history'
import { diskPowerSummary, formatByteRate, formatBytes, formatPercent } from '@/lib/metrics'

const EMPTY_POINTS: Point[] = []
const MetricChart = lazy(() => import('@/components/MetricChart').then(module => ({ default: module.MetricChart })))
const date = (v: string | null) => v ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(v)) : '暂无数据'
const uptime = (s: number) => `${Math.floor(s / 86400)} 天 ${Math.floor(s % 86400 / 3600)} 小时`
const toPoint = (d: DeviceState): Point => ({ time: +new Date(d.snapshot.timestamp), cpu: d.snapshot.cpu.usage_percent, memory: d.snapshot.memory.usage_percent, rx: d.snapshot.networks.reduce((a, n) => a + n.rx_bytes_per_second, 0), tx: d.snapshot.networks.reduce((a, n) => a + n.tx_bytes_per_second, 0), temperature: d.snapshot.cpu.temperature_celsius, temperature_time: d.snapshot.cpu.sensor_collected_at ? +new Date(d.snapshot.cpu.sensor_collected_at) : null })

export default function App() {
  const [authState, setAuthState] = useState<'checking' | 'login' | 'legacy' | 'authenticated' | 'unavailable'>('checking')
  const [authMessage, setAuthMessage] = useState('')
  const [userRole, setUserRole] = useState<'owner' | 'viewer' | null>(null)
  const [devices, setDevices] = useState<DeviceState[]>([]), [selectedID, setSelectedID] = useState('')
  const [page, setPage] = useState<AppPage>('dashboard')
  const [history, setHistory] = useState<Record<string, Point[]>>({}), [loading, setLoading] = useState(true)
  const [error, setError] = useState(''), [connected, setConnected] = useState(false)
  const [dark, setDark] = useState(() => localStorage.theme === 'dark' || (!localStorage.theme && matchMedia('(prefers-color-scheme: dark)').matches))
  const [job, setJob] = useState<RefreshJob | null>(null)
  const merge = useCallback((state: DeviceState) => {
    const id = state.snapshot.device.id
    setDevices(ds => [...ds.filter(d => d.snapshot.device.id !== id), state].sort((a, b) => a.snapshot.device.id.localeCompare(b.snapshot.device.id)))
    setSelectedID(old => old || id)
    setHistory(h => ({ ...h, [id]: appendPoint(h[id] || [], toPoint(state)) }))
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/auth/session', { credentials: 'same-origin', signal: controller.signal }).then(async response => {
      if (response.status === 404) { setAuthState('legacy'); return }
      if (response.status === 401) {
        setDevices([])
        setHistory({})
        setSelectedID('')
        setUserRole(null)
        setAuthState('login')
        return
      }
      if (response.status === 403) throw new Error('认证要求通过 HTTPS 访问。')
      if (!response.ok) throw new Error(`认证服务返回 ${response.status}`)
      const session = await response.json() as { user?: { role?: 'owner' | 'viewer' } }
      setUserRole(session.user?.role === 'owner' || session.user?.role === 'viewer' ? session.user.role : null)
      setAuthState('authenticated')
    }).catch(error => {
      if (error.name !== 'AbortError') {
        setAuthMessage(error.message || '无法连接认证服务。')
        setAuthState('unavailable')
      }
    })
    return () => controller.abort()
  }, [])
  useEffect(() => {
    if (authState !== 'legacy' && authState !== 'authenticated') return
    const controller = new AbortController()
    fetch('/api/v1/devices', { credentials: 'same-origin', signal: controller.signal }).then(response => {
      if (response.status === 401 && authState === 'authenticated') {
        setDevices([])
        setHistory({})
        setSelectedID('')
        setUserRole(null)
        setAuthState('login')
        return null
      }
      if (!response.ok) throw new Error(`${response.status}`)
      return response.json() as Promise<DeviceState[]>
    }).then(rows => {
      if (!rows) return
      setDevices(rows)
      if (rows[0]) {
        setSelectedID(rows[0].snapshot.device.id)
        setHistory(Object.fromEntries(rows.map(device => [device.snapshot.device.id, [toPoint(device)]])))
      }
    }).catch(error => { if (error.name !== 'AbortError') setError(`无法读取设备：${error.message}`) }).finally(() => setLoading(false))
    const stream = new EventSource('/api/v1/events'); stream.onopen = () => setConnected(true); stream.onerror = () => setConnected(false)
    stream.addEventListener('metrics', e => { setConnected(true); merge(JSON.parse((e as MessageEvent).data)) })
    return () => { controller.abort(); stream.close() }
  }, [authState, merge])
  useEffect(() => { document.documentElement.classList.toggle('dark', dark); localStorage.theme = dark ? 'dark' : 'light' }, [dark])
  useEffect(() => { if (authState !== 'legacy' && authState !== 'authenticated') return; const ping = () => { if (document.visibilityState === 'visible') fetch('/api/v1/presence', { method: 'POST', credentials: 'same-origin' }).catch(() => undefined) }; ping(); const timer = setInterval(ping, 30000); document.addEventListener('visibilitychange', ping); return () => { clearInterval(timer); document.removeEventListener('visibilitychange', ping) } }, [authState])
  useEffect(() => {
    if (authState !== 'authenticated') return
    const timer = setInterval(() => {
      fetch('/api/v1/auth/session', { credentials: 'same-origin' }).then(response => {
        if (response.status === 401) {
          setDevices([])
          setHistory({})
          setSelectedID('')
          setUserRole(null)
          setAuthState('login')
        }
        else if (response.ok) return response.json() as Promise<{ user?: { role?: 'owner' | 'viewer' } }>
        return null
      }).then(session => {
        if (session) setUserRole(session.user?.role === 'owner' || session.user?.role === 'viewer' ? session.user.role : null)
      }).catch(() => undefined)
    }, 60_000)
    return () => clearInterval(timer)
  }, [authState])

  const selected = devices.find(d => d.snapshot.device.id === selectedID) || devices[0], points = history[selected?.snapshot.device.id] || EMPTY_POINTS
  const chart = useMemo(() => chartOption(points, dark, 'resource'), [points, dark]), networkChart = useMemo(() => chartOption(points, dark, 'network'), [points, dark]), temperatureChart = useMemo(() => chartOption(points, dark, 'temperature'), [points, dark])
  async function refresh() {
    if (!selected || !confirm('主动刷新可能唤醒休眠中的 HDD，是否继续？')) return
    const response = await fetch(`/api/v1/devices/${encodeURIComponent(selected.snapshot.device.id)}/refresh`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ collectors: ['disk'], allow_wake: true }) })
    if (!response.ok) { setError('无法创建磁盘刷新任务'); return }
    const created: RefreshJob = await response.json(); setJob(created)
    const poll = setInterval(async () => { const r = await fetch(`/api/v1/refresh/${created.request_id}`); if (!r.ok) return; const next: RefreshJob = await r.json(); setJob(next); if (['completed', 'failed', 'expired'].includes(next.status)) clearInterval(poll) }, 1000)
    setTimeout(() => clearInterval(poll), 31 * 60_000)
  }
  async function logout() {
    try {
      const response = await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin' })
      if (!response.ok && response.status !== 401) throw new Error(`认证服务返回 ${response.status}`)
      setDevices([])
      setHistory({})
      setSelectedID('')
      setConnected(false)
      setJob(null)
      setError('')
      setUserRole(null)
      setAuthState('login')
    } catch (error) {
      setError(`注销失败：${error instanceof Error ? error.message : '无法连接认证服务'}`)
    }
  }
  async function authenticated() {
    setError('')
    setLoading(true)
    try {
      const response = await fetch('/api/v1/auth/session', { credentials: 'same-origin' })
      if (!response.ok) throw new Error(`认证服务返回 ${response.status}`)
      const session = await response.json() as { user?: { role?: 'owner' | 'viewer' } }
      setDevices([])
      setHistory({})
      setSelectedID('')
      setUserRole(session.user?.role === 'owner' || session.user?.role === 'viewer' ? session.user.role : null)
      setAuthState('authenticated')
    } catch (error) {
      setAuthMessage(error instanceof Error ? error.message : '无法读取登录会话。')
      setAuthState('unavailable')
    }
  }

  if (authState === 'checking') return <main className="grid min-h-svh place-items-center bg-background text-sm text-muted-foreground" role="status">正在检查登录状态…</main>
  if (authState === 'login') return <LoginScreen onAuthenticated={() => { void authenticated() }}/>
  if (authState === 'unavailable') return <main className="grid min-h-svh place-items-center bg-background px-4 text-foreground"><p role="alert" className="max-w-md rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">{authMessage}</p></main>

  return <div className="min-h-svh bg-background text-foreground">
    <header className="sticky top-0 z-20 border-b bg-background/90 backdrop-blur"><div className="mx-auto flex max-w-[1800px] items-center justify-between px-4 py-3 sm:px-6"><div className="flex items-center gap-3"><span className="grid size-9 place-items-center rounded-xl bg-emerald-500 text-white"><Activity size={20}/></span><div><h1 className="font-semibold">Home Lab Monitor</h1><p className="text-xs text-muted-foreground">家庭实验室实时状态</p></div></div><div className="flex items-center gap-2"><Badge variant="outline" className="gap-1.5"><span className={`size-2 rounded-full ${connected ? 'bg-emerald-500' : 'bg-amber-500'}`}/>{connected ? '实时连接' : '正在重连'}</Badge><Button variant="ghost" size="icon" onClick={() => setDark(v => !v)} aria-label="切换主题">{dark ? <Sun/> : <Moon/>}</Button>{authState === 'authenticated' && <Button variant="outline" size="sm" onClick={() => void logout()}><LogOut/>注销</Button>}</div></div></header>
    <AppNavigation current={page} onSelect={setPage} showOwnerPages={userRole === 'owner'} showAuthenticatedPages={authState === 'authenticated'}/>
    <main className={`mx-auto grid max-w-[1800px] gap-5 p-4 sm:p-6 ${page === 'dashboard' ? 'lg:grid-cols-[260px_minmax(0,1fr)]' : ''}`}>
      {page === 'dashboards' && authState === 'authenticated' ? <DashboardManagement role={userRole === 'owner' ? 'owner' : 'viewer'} devices={devices} dark={dark}/> : page === 'alerts' && authState === 'authenticated' ? <AlertsOverview devices={devices} role={userRole === 'owner' ? 'owner' : 'viewer'}/> : page === 'settings' && userRole === 'owner' ? <OwnerUserManagement/> : page === 'network' ? <NetworkOverview devices={devices}/> : page === 'explore' ? <ExploreOverview devices={devices} history={history} dark={dark}/> : <>
      <aside><div className="mb-3 flex justify-between"><h2 className="text-sm font-semibold">设备</h2><Badge variant="secondary">{devices.length}</Badge></div><div className="flex gap-2 overflow-x-auto pb-2 lg:flex-col">{devices.map(d => <button key={d.snapshot.device.id} onClick={() => setSelectedID(d.snapshot.device.id)} className={`min-w-56 rounded-xl border p-3 text-left transition lg:min-w-0 ${selected?.snapshot.device.id === d.snapshot.device.id ? 'border-emerald-500 bg-emerald-500/5' : 'hover:bg-muted'}`}><div className="flex justify-between gap-2"><b className="truncate text-sm">{d.snapshot.device.id}</b><Status value={d.status}/></div><p className="mt-1 truncate text-xs text-muted-foreground">{d.snapshot.device.display_name} · {d.snapshot.device.hostname}</p></button>)}</div></aside>
      <section className="min-w-0">{loading ? <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">{[1,2,3,4].map(i => <Skeleton key={i} className="h-32"/>)}</div> : !selected ? <Empty title={error || '还没有设备'} detail="启动 Agent 后，设备将在这里出现。"/> : <>
        <div className="mb-5 flex flex-wrap items-end justify-between gap-3"><div><div className="flex items-center gap-2"><h2 className="text-2xl font-semibold">{selected.snapshot.device.id}</h2><Status value={selected.status}/></div><p className="mt-1 text-sm text-muted-foreground">{selected.snapshot.device.display_name} · {selected.snapshot.device.hostname} · 更新于 {date(selected.received_at)}</p></div><Badge variant="outline">{selected.snapshot.sampling_mode === 'active' ? '实时模式' : '节能模式'} · {selected.snapshot.sampling_interval_seconds} 秒</Badge></div>
        {error && <p className="mb-4 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">{error}</p>}
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4"><Metric icon={<Cpu/>} label="CPU 使用率" value={formatPercent(selected.snapshot.cpu.usage_percent)} sub={`${selected.snapshot.cpu.logical_cores} 逻辑核心`}/><Metric icon={<MemoryStick/>} label="内存使用率" value={formatPercent(selected.snapshot.memory.usage_percent)} sub={`${formatBytes(selected.snapshot.memory.used_bytes)} / ${formatBytes(selected.snapshot.memory.total_bytes)}`}/><Metric icon={<Thermometer/>} label="CPU 温度" value={selected.snapshot.cpu.temperature_celsius == null ? '不可用' : `${selected.snapshot.cpu.temperature_celsius.toFixed(0)} °C`} sub={`采集于 ${date(selected.snapshot.cpu.sensor_collected_at)}`}/><Metric icon={<Clock3/>} label="运行时间" value={uptime(selected.snapshot.system.uptime_seconds)} sub={`负载 ${selected.snapshot.system.load1.toFixed(2)} / ${selected.snapshot.system.load5.toFixed(2)} / ${selected.snapshot.system.load15.toFixed(2)}`}/></div>
        <div className="mt-4 grid gap-4 xl:grid-cols-2"><ChartCard title="资源趋势" icon={<Gauge/>} option={chart}/>{selected.snapshot.cpu.temperature_celsius != null && <ChartCard title="CPU 温度趋势" icon={<Thermometer/>} option={temperatureChart}/>}<ChartCard title="网络吞吐" icon={<Wifi/>} option={networkChart} className={selected.snapshot.cpu.temperature_celsius != null ? 'xl:col-span-2' : ''}/></div>
        <Card className="mt-4"><CardHeader className="flex-row items-center justify-between"><div><CardTitle className="flex items-center gap-2 text-base"><HardDrive size={18}/>文件系统</CardTitle>{job && <p className="mt-1 text-xs text-muted-foreground">刷新任务：{job.status}{job.error ? ` · ${job.error}` : ''}</p>}</div><Button variant="outline" size="sm" onClick={refresh}><RefreshCw/>主动刷新</Button></CardHeader><CardContent><div className="grid gap-3 md:grid-cols-2">{selected.snapshot.disks.length ? selected.snapshot.disks.map(d => <DiskCard key={d.mount_point} disk={d}/>) : <div className="text-sm text-muted-foreground"><p>暂无可识别的文件系统。</p><p className="mt-1 text-xs">低功耗策略不会为发现磁盘而唤醒休眠中的 HDD。</p></div>}</div></CardContent></Card>
        <Card className="mt-4"><CardHeader><CardTitle className="flex items-center gap-2 text-base"><Wifi size={18}/>网络接口</CardTitle></CardHeader><CardContent><div className="overflow-x-auto"><table className="w-full min-w-[560px] text-sm"><thead className="text-left text-xs text-muted-foreground"><tr className="border-b"><th className="pb-2">接口</th><th>接收速率</th><th>发送速率</th><th>累计接收</th><th>累计发送</th></tr></thead><tbody>{selected.snapshot.networks.map(n => <tr key={n.interface} className="border-b last:border-0"><td className="py-3 font-medium">{n.interface}</td><td>{formatByteRate(n.rx_bytes_per_second)}</td><td>{formatByteRate(n.tx_bytes_per_second)}</td><td>{formatBytes(n.rx_bytes)}</td><td>{formatBytes(n.tx_bytes)}</td></tr>)}</tbody></table></div></CardContent></Card>
      </>}</section>
      </>}
    </main>
  </div>
}

function chartOption(points: Point[], dark: boolean, mode: ChartMode) {
  const network = mode === 'network', temperature = mode === 'temperature'
  const series = temperature ? [{ key: 'temperature' as const, name: 'CPU 温度' }] : network ? [{ key: 'rx' as const, name: '接收速率' }, { key: 'tx' as const, name: '发送速率' }] : [{ key: 'cpu' as const, name: 'CPU 使用率' }, { key: 'memory' as const, name: '内存使用率' }]
  const temperatureChart = temperatureChartData(points)
  const tick = (value: number) => new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(new Date(value))
  return {
    animation: false,
    tooltip: { trigger: 'axis' as const, confine: true, transitionDuration: 0, formatter: (params: unknown) => chartTooltip(params, mode, tick) },
    legend: {
      top: 0, left: 'center', itemWidth: 16, itemHeight: 8,
      textStyle: { color: dark ? '#cbd5e1' : '#475569', fontSize: 12 },
    },
    grid: { left: 8, right: 10, top: 38, bottom: 12, containLabel: true },
    xAxis: {
      type: 'time' as const, min: temperature ? temperatureChart.min : undefined, max: temperature ? temperatureChart.max : undefined,
      boundaryGap: [0, 0] as [number, number], splitNumber: 3,
      axisTick: { show: false },
      axisLabel: {
        color: dark ? '#94a3b8' : '#64748b', fontSize: 10,
        hideOverlap: true, margin: 10, formatter: tick,
      },
    },
    yAxis: {
      type: 'value' as const, min: temperature ? undefined : 0, max: network || temperature ? undefined : 100, splitNumber: 4,
      axisTick: { show: false }, axisLine: { show: false },
      axisLabel: {
        formatter: network ? (value: number) => formatByteRate(value) : temperature ? '{value} °C' : '{value}%',
        color: dark ? '#94a3b8' : '#64748b', fontSize: 10,
      },
      splitLine: { lineStyle: { color: dark ? '#1e293b' : '#eef2f7' } },
    },
    series: series.map(({ key, name }, index) => ({
      name, type: 'line' as const, smooth: true, showSymbol: temperature, symbolSize: temperature ? 8 : 4,
      data: temperature ? temperatureChart.data : points.map(point => [point.time, point[key]]),
      lineStyle: { color: temperature ? '#f97316' : ['#22c55e', '#3b82f6'][index], width: 2 },
      itemStyle: { color: temperature ? '#f97316' : ['#22c55e', '#3b82f6'][index] },
    })),
  }
}
function Status({ value }: { value: DeviceState['status'] }) { const style = { online: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300', stale: 'bg-amber-500/15 text-amber-700 dark:text-amber-300', offline: 'bg-slate-500/15 text-slate-600 dark:text-slate-300' }; return <span className={`rounded-full px-2 py-0.5 text-[11px] ${style[value]}`}>{value === 'online' ? '在线' : value === 'stale' ? '延迟' : '离线'}</span> }
function Metric({ icon, label, value, sub }: { icon: ReactNode; label: string; value: string; sub: string }) { return <Card><CardContent className="pt-5"><div className="flex justify-between text-sm text-muted-foreground"><span>{label}</span><span className="[&>svg]:size-4">{icon}</span></div><p className="mt-3 text-3xl font-semibold tabular-nums">{value}</p><p className="mt-2 truncate text-xs text-muted-foreground">{sub}</p></CardContent></Card> }
function DiskCard({ disk }: { disk: DiskMetric }) {
  const hasCapacity = disk.capacity_available ?? disk.total_bytes > 0
  const media = disk.media_type === 'hdd' ? 'HDD' : disk.media_type === 'ssd' ? 'SSD' : '介质未知'
  const power = diskPowerSummary(disk)
  const powerStyle = power.state === 'standby' ? 'border-emerald-500/30 text-emerald-600 dark:text-emerald-300' : power.state === 'active' ? 'border-amber-500/30 text-amber-600 dark:text-amber-300' : ''
  return <div className="rounded-lg border p-3">
    <div className="flex justify-between gap-3"><div className="min-w-0"><div className="flex flex-wrap items-center gap-2"><p className="truncate text-sm font-medium">{disk.mount_point}</p><Badge variant="secondary" className="h-5 px-1.5 text-[10px]">{media}</Badge>{disk.media_type === 'hdd' && <Badge variant="outline" className={`h-5 px-1.5 text-[10px] ${powerStyle}`}>{power.label}</Badge>}</div><p className="truncate text-xs text-muted-foreground">{disk.device} · {disk.filesystem}</p></div>{hasCapacity && <span className="text-sm">{formatPercent(disk.usage_percent)}</span>}</div>
    {hasCapacity ? <><Progress value={disk.usage_percent} className="my-3"/><div className="flex flex-wrap justify-between gap-1 text-xs text-muted-foreground"><span>{formatBytes(disk.used_bytes)} / {formatBytes(disk.total_bytes)}</span><span>{disk.collection_mode === 'active' ? '主动' : '被动'} · {date(disk.collected_at)}</span></div></> : <div className="my-3 rounded-md bg-muted/60 px-3 py-2 text-xs text-muted-foreground">容量读取已按低功耗策略暂缓；这不代表已确认 HDD 处于休眠状态。需要时可使用“主动刷新”。</div>}
    <details className="mt-3 border-t pt-2 text-xs"><summary className="cursor-pointer select-none text-muted-foreground">硬件详情</summary><dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5"><dt className="text-muted-foreground">介质</dt><dd>{media}</dd><dt className="text-muted-foreground">底层设备</dt><dd>{disk.physical_devices?.map(d => d.name).join(', ') || '暂无数据'}</dd><dt className="text-muted-foreground">型号</dt><dd>{disk.physical_devices?.map(d => [d.vendor, d.model].filter(Boolean).join(' ')).filter(Boolean).join(', ') || '内核未提供'}</dd><dt className="text-muted-foreground">电源状态</dt><dd>{power.label}{power.checkedAt ? ` · ${date(power.checkedAt)}` : ''}</dd><dt className="text-muted-foreground">转速</dt><dd>{disk.media_type === 'hdd' ? '未安全提供（不主动探测）' : '不适用'}</dd><dt className="text-muted-foreground">磁头 / SMART</dt><dd>未采集，避免后台检测唤醒磁盘</dd></dl></details>
  </div>
}
function ChartCard({ title, icon, option, className = '' }: { title: string; icon: ReactNode; option: ReturnType<typeof chartOption>; className?: string }) { return <Card className={className}><CardHeader><CardTitle className="flex items-center gap-2 text-base"><span className="[&>svg]:size-4">{icon}</span>{title}</CardTitle></CardHeader><CardContent><Suspense fallback={<Skeleton className="h-56 sm:h-64"/>}><MetricChart option={option} label={title}/></Suspense></CardContent></Card> }
function Empty({ title, detail }: { title: string; detail: string }) { return <div className="grid min-h-[55vh] place-items-center rounded-2xl border border-dashed p-8 text-center"><div><Server className="mx-auto mb-4 text-muted-foreground"/><h2 className="font-semibold">{title}</h2><p className="mt-1 text-sm text-muted-foreground">{detail}</p></div></div> }
