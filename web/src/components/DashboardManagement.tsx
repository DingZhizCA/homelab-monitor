/**
 * 受控仪表盘的读取与编辑入口。
 * 只写入 Dashboard API 文档，不提供代码表达式或设备采集控制。
 */
import { useEffect, useRef, useState } from 'react'
import { PluginWidgetHistoryPreview } from '@/components/PluginWidgetHistoryPreview'
import { descriptorReference, pluginMetricKey, pluginMetricLabel } from '@/lib/pluginMetrics'
import type { PluginMetricDescriptor, PluginMetricReference } from '@/types'

type Scope = 'personal' | 'shared' | 'template'
type WidgetKind = 'metric_card' | 'time_series' | 'status' | 'device_list' | 'traffic_ranking' | 'categorized_traffic' | 'wifi_clients' | 'tailnet_clients' | 'alert_list' | 'topology'

interface Widget {
  id: string
  kind: WidgetKind
  title: string
  descriptor_id?: string
  device_id?: string
  plugin_metric?: PluginMetricReference
}

interface Placement { widget_id: string; x: number; y: number; width: number; height: number }
interface Layout { breakpoint: 'desktop' | 'mobile'; items: Placement[] }
interface DocumentModel { widgets: Widget[]; layouts: Layout[] }
interface Revision { number: number; document: DocumentModel }
interface DashboardModel { id: string; name: string; scope: Scope; draft?: Revision; published?: Revision }
interface Descriptor { id: string; name: string; unit: string }
interface MetricPreview {
  descriptor: Descriptor
  points: { value?: number; dimensions?: Record<string, string>; quality: 'fresh' | 'stale' | 'missing' | 'unsupported' }[]
  sampled_at: string
}
interface PreviewState { loading: boolean; result?: MetricPreview; error?: string }

const emptyDocument = (): DocumentModel => ({ widgets: [], layouts: [] })

export function DashboardManagement({ role, devices = [], dark = false }: { role: 'owner' | 'viewer'; devices?: { snapshot: { device: { id: string; display_name: string } } }[]; dark?: boolean }) {
  const [items, setItems] = useState<DashboardModel[]>([])
  const [selected, setSelected] = useState<DashboardModel | null>(null)
  const [document, setDocument] = useState<DocumentModel>(emptyDocument)
  const [descriptors, setDescriptors] = useState<Descriptor[]>([])
  const [descriptorID, setDescriptorID] = useState('')
  const [deviceID, setDeviceID] = useState('')
  const [metricSource, setMetricSource] = useState<'builtin' | 'plugin'>('builtin')
  const [pluginDescriptors, setPluginDescriptors] = useState<PluginMetricDescriptor[]>([])
  const [pluginCatalogDeviceID, setPluginCatalogDeviceID] = useState('')
  const [pluginDescriptorID, setPluginDescriptorID] = useState('')
  const [pluginCatalogMessage, setPluginCatalogMessage] = useState(devices.length ? '正在读取已观测插件指标…' : '选择设备后读取插件指标。')
  const [name, setName] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [previews, setPreviews] = useState<Record<string, PreviewState>>({})
  const [historyOpen, setHistoryOpen] = useState(false)
  const [historyLoading, setHistoryLoading] = useState(false)
  const [historyError, setHistoryError] = useState('')
  const [publishedRevisions, setPublishedRevisions] = useState<Revision[]>([])
  const [previewedHistoryRevision, setPreviewedHistoryRevision] = useState<number | null>(null)
  const historyRequest = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    Promise.all([
      request<{ dashboards: DashboardModel[] }>('/api/v1/dashboards', { signal: controller.signal }),
      request<{ descriptors: Descriptor[] }>('/api/v1/dashboard/metrics/descriptors', { signal: controller.signal }),
    ]).then(([dashboardResult, descriptorResult]) => {
      setItems(dashboardResult.dashboards)
      setDescriptors(descriptorResult.descriptors)
      setDescriptorID(descriptorResult.descriptors[0]?.id || '')
      setError('')
    }).catch(reason => {
      if (reason.name !== 'AbortError') setError(reason instanceof Error ? reason.message : '无法读取仪表盘服务。')
    }).finally(() => setLoading(false))
    return () => {
      controller.abort()
      historyRequest.current?.abort()
    }
  }, [])

  const currentDeviceID = devices.some(device => device.snapshot.device.id === deviceID)
    ? deviceID
    : devices[0]?.snapshot.device.id || ''
  const canEdit = selected?.scope === 'personal' || (selected?.scope === 'shared' && role === 'owner')

  useEffect(() => {
    if (!currentDeviceID || !canEdit) return
    const controller = new AbortController()
    const query = new URLSearchParams({ device_id: currentDeviceID })
    request<{ descriptors: PluginMetricDescriptor[] }>(`/api/v1/dashboard/plugin-metrics/descriptors?${query}`, { signal: controller.signal })
      .then(result => {
        setPluginDescriptors(result.descriptors)
        setPluginCatalogDeviceID(currentDeviceID)
        setPluginDescriptorID(result.descriptors[0] ? pluginMetricKey(result.descriptors[0]) : '')
        setPluginCatalogMessage(result.descriptors.length ? '' : '该设备尚无已观测插件指标。')
      })
      .catch(reason => {
        if (!(reason instanceof Error && reason.name === 'AbortError')) setPluginCatalogMessage(reason instanceof Error ? reason.message : '无法读取插件指标目录。')
      })
    return () => controller.abort()
  }, [canEdit, currentDeviceID])

  const availablePluginDescriptors = pluginCatalogDeviceID === currentDeviceID ? pluginDescriptors : []

  async function openDashboard(id: string) {
    historyRequest.current?.abort()
    historyRequest.current = null
    setHistoryLoading(false)
    if (!id) { setSelected(null); setDocument(emptyDocument()); setHistoryOpen(false); setPublishedRevisions([]); setPreviewedHistoryRevision(null); return }
    setBusy(true)
    setError('')
    setHistoryOpen(false)
    setPublishedRevisions([])
    setPreviewedHistoryRevision(null)
    setHistoryError('')
    try {
      const current = await request<DashboardModel>(`/api/v1/dashboards/${encodeURIComponent(id)}`)
      setSelected(current)
      setDocument(current.draft?.document || current.published?.document || emptyDocument())
      setName(current.name)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '无法打开仪表盘。')
    } finally { setBusy(false) }
  }

  async function create(scope: 'personal' | 'shared') {
    historyRequest.current?.abort()
    historyRequest.current = null
    setHistoryLoading(false)
    setHistoryOpen(false)
    setPublishedRevisions([])
    setPreviewedHistoryRevision(null)
    setBusy(true)
    setError('')
    try {
      const created = await request<DashboardModel>('/api/v1/dashboards', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: name.trim(), scope, document: emptyDocument() }),
      })
      setItems(current => [...current, created])
      setSelected(created)
      setDocument(created.draft?.document || emptyDocument())
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '无法创建仪表盘。')
    } finally { setBusy(false) }
  }

  function addWidget() {
    if (!currentDeviceID) return
    const descriptor = descriptors.find(item => item.id === descriptorID)
    const pluginDescriptor = availablePluginDescriptors.find(item => pluginMetricKey(item) === pluginDescriptorID)
    if ((metricSource === 'builtin' && !descriptor) || (metricSource === 'plugin' && !pluginDescriptor)) return
    const widgetID = crypto.randomUUID()
    const widget: Widget = metricSource === 'builtin' && descriptor
      ? { id: widgetID, kind: 'time_series', title: descriptor.name, descriptor_id: descriptor.id, device_id: currentDeviceID }
      : { id: widgetID, kind: 'time_series', title: pluginMetricLabel(pluginDescriptor!), device_id: currentDeviceID, plugin_metric: descriptorReference(pluginDescriptor!) }
    const desktop = document.layouts.find(layout => layout.breakpoint === 'desktop') || { breakpoint: 'desktop' as const, items: [] }
    const mobile = document.layouts.find(layout => layout.breakpoint === 'mobile') || { breakpoint: 'mobile' as const, items: [] }
    setDocument({
      widgets: [...document.widgets, widget],
      layouts: [
        { ...desktop, items: [...desktop.items, { widget_id: widgetID, x: 0, y: layoutEnd(desktop.items), width: 4, height: 2 }] },
        { ...mobile, items: [...mobile.items, { widget_id: widgetID, x: 0, y: layoutEnd(mobile.items), width: 1, height: 2 }] },
      ],
    })
  }

  function removeWidget(widgetID: string) {
    setDocument(current => ({
      widgets: current.widgets.filter(widget => widget.id !== widgetID),
      layouts: current.layouts.map(layout => ({ ...layout, items: layout.items.filter(item => item.widget_id !== widgetID) })),
    }))
  }

  async function previewWidget(widget: Widget) {
    if (!widget.descriptor_id || !widget.device_id) return
    setPreviews(current => ({ ...current, [widget.id]: { loading: true } }))
    const query = new URLSearchParams({ descriptor_id: widget.descriptor_id, device_id: widget.device_id })
    try {
      const result = await request<MetricPreview>(`/api/v1/dashboard/metrics/latest?${query}`)
      setPreviews(current => ({ ...current, [widget.id]: { loading: false, result } }))
    } catch (reason) {
      setPreviews(current => ({ ...current, [widget.id]: { loading: false, error: reason instanceof Error ? reason.message : '无法读取指标当前值。' } }))
    }
  }

  async function save() {
    if (!selected) return
    const revision = selected.draft?.number
    if (!revision) { setError('服务未返回可编辑的草稿版本，请重新加载仪表盘。'); return }
    setBusy(true)
    setError('')
    const operation = selected.scope === 'personal' ? 'personal' : 'draft'
    try {
      const saved = await request<DashboardModel>(`/api/v1/dashboards/${encodeURIComponent(selected.id)}/${operation}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ expected_revision: revision, document }),
      })
      setSelected(saved)
      setItems(current => current.map(item => item.id === saved.id ? saved : item))
      setDocument(saved.draft?.document || document)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存失败。')
    } finally { setBusy(false) }
  }

  async function publish() {
    if (!selected || selected.scope !== 'shared' || role !== 'owner' || !selected.draft?.number) return
    setBusy(true)
    setError('')
    try {
      const published = await request<DashboardModel>(`/api/v1/dashboards/${encodeURIComponent(selected.id)}/publish`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ expected_draft_revision: selected.draft.number }),
      })
      setSelected(published)
      setItems(current => current.map(item => item.id === published.id ? published : item))
      setDocument(published.draft?.document || published.published?.document || emptyDocument())
      if (historyOpen) await refreshPublishedHistory(published.id)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '发布失败。')
    } finally { setBusy(false) }
  }

  async function copy() {
    historyRequest.current?.abort()
    historyRequest.current = null
    setHistoryLoading(false)
    if (!selected || selected.scope === 'personal') return
    setBusy(true)
    setError('')
    try {
      const copied = await request<DashboardModel>(`/api/v1/dashboards/${encodeURIComponent(selected.id)}/copy`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: name.trim() }),
      })
      setItems(current => [...current, copied])
      setSelected(copied)
      setDocument(copied.draft?.document || emptyDocument())
      setHistoryOpen(false)
      setPublishedRevisions([])
      setPreviewedHistoryRevision(null)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '复制失败。')
    } finally { setBusy(false) }
  }

  async function refreshPublishedHistory(dashboardID: string) {
    historyRequest.current?.abort()
    const controller = new AbortController()
    historyRequest.current = controller
    setHistoryLoading(true)
    setHistoryError('')
    try {
      const result = await request<{ revisions: Revision[] }>(`/api/v1/dashboards/${encodeURIComponent(dashboardID)}/revisions`, { signal: controller.signal })
      if (historyRequest.current === controller && !controller.signal.aborted) setPublishedRevisions(result.revisions)
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === 'AbortError')) setHistoryError(reason instanceof Error ? reason.message : '无法读取发布历史。')
    } finally {
      if (historyRequest.current === controller) {
        historyRequest.current = null
        setHistoryLoading(false)
      }
    }
  }

  async function togglePublishedHistory() {
    if (!selected || selected.scope !== 'shared' || role !== 'owner') return
    if (historyOpen) {
      historyRequest.current?.abort()
      historyRequest.current = null
      setHistoryOpen(false)
      setHistoryLoading(false)
      setPreviewedHistoryRevision(null)
      return
    }
    setHistoryOpen(true)
    setPreviewedHistoryRevision(null)
    await refreshPublishedHistory(selected.id)
  }

  async function rollback(targetRevision: number) {
    if (!selected || selected.scope !== 'shared' || role !== 'owner' || !selected.published?.number || targetRevision === selected.published.number) return
    setBusy(true)
    setError('')
    try {
      const rolledBack = await request<DashboardModel>(`/api/v1/dashboards/${encodeURIComponent(selected.id)}/rollback`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target_published_revision: targetRevision, expected_published_revision: selected.published.number }),
      })
      setSelected(rolledBack)
      setItems(current => current.map(item => item.id === rolledBack.id ? rolledBack : item))
      setDocument(rolledBack.draft?.document || rolledBack.published?.document || emptyDocument())
      setHistoryOpen(true)
      setPreviewedHistoryRevision(null)
      await refreshPublishedHistory(rolledBack.id)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '回滚失败。')
    } finally { setBusy(false) }
  }

  const previewedHistory = publishedRevisions.find(revision => revision.number === previewedHistoryRevision)
  const canAddWidget = currentDeviceID !== '' && (metricSource === 'builtin' ? descriptorID !== '' : pluginDescriptorID !== '' && availablePluginDescriptors.length > 0)

  return <section aria-labelledby="dashboards-title" className="min-w-0">
    <div className="mb-5"><h2 id="dashboards-title" className="text-2xl font-semibold">仪表盘</h2><p className="mt-1 text-sm text-muted-foreground">个人布局只影响当前账户；共享草稿由 Owner 发布后对 Viewer 可见。</p></div>
    {error && <p role="alert" className="mb-4 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">{error}</p>}
    {loading ? <p role="status" className="text-sm text-muted-foreground">正在读取仪表盘…</p> : <div className="grid gap-5 lg:grid-cols-[280px_minmax(0,1fr)]">
      <aside className="space-y-3 rounded-xl border p-4">
        <label className="block text-sm">仪表盘名称<input aria-label="仪表盘名称" value={name} onChange={event => setName(event.target.value)} className="mt-1 w-full rounded-md border bg-background px-3 py-2"/></label>
        <button type="button" disabled={busy || !name.trim()} onClick={() => void create('personal')} className="w-full rounded-md border px-3 py-2 text-sm disabled:opacity-50">新建个人仪表盘</button>
        {role === 'owner' && <button type="button" disabled={busy || !name.trim()} onClick={() => void create('shared')} className="w-full rounded-md border px-3 py-2 text-sm disabled:opacity-50">新建共享草稿</button>}
        <h3 className="pt-2 text-sm font-medium">已有仪表盘</h3>
        {items.length === 0 ? <p className="text-xs text-muted-foreground">暂无仪表盘</p> : items.map(item => <button key={item.id} type="button" onClick={() => void openDashboard(item.id)} className="block w-full rounded-md px-3 py-2 text-left text-sm hover:bg-muted">{item.name}<span className="ml-2 text-xs text-muted-foreground">{item.scope === 'personal' ? '个人' : item.scope === 'shared' ? '共享' : '模板'}</span></button>)}
      </aside>
      <div className="min-w-0 space-y-4 rounded-xl border p-4">
        {!selected ? <p className="py-8 text-center text-sm text-muted-foreground">选择仪表盘，或创建一个新的个人布局。</p> : <>
          <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold">{selected.name}</h3><div className="flex flex-wrap gap-2">
            {selected.scope !== 'personal' && <button type="button" disabled={busy} onClick={() => void copy()} className="rounded-md border px-3 py-2 text-sm">复制为个人版</button>}
            {selected.scope === 'shared' && role === 'owner' && <button type="button" disabled={busy || !selected.draft?.number} onClick={() => void publish()} className="rounded-md border px-3 py-2 text-sm">发布草稿</button>}
            {selected.scope === 'shared' && role === 'owner' && <button type="button" disabled={busy || !selected.published?.number} aria-expanded={historyOpen} onClick={() => void togglePublishedHistory()} className="rounded-md border px-3 py-2 text-sm">{historyOpen ? '隐藏发布历史' : '查看发布历史'}</button>}
            {canEdit && <button type="button" disabled={busy} onClick={() => void save()} className="rounded-md bg-emerald-600 px-3 py-2 text-sm text-white disabled:opacity-50">保存布局</button>}
          </div></div>
          {historyOpen && <section aria-labelledby="published-history-title" className="rounded-lg border p-3">
            <h4 id="published-history-title" className="text-sm font-medium">已发布版本</h4>
            <p className="mt-1 text-xs text-muted-foreground">回滚会创建新的发布版本，并保留现有历史。</p>
            {historyLoading && <p role="status" className="mt-2 text-xs text-muted-foreground">正在读取发布历史…</p>}
            {historyError && <p role="alert" className="mt-2 text-xs text-destructive">{historyError}</p>}
            {!historyLoading && !historyError && publishedRevisions.length === 0 && <p className="mt-2 text-xs text-muted-foreground">暂无已发布版本。</p>}
            <ul className="mt-2 space-y-2">{publishedRevisions.map(revision => <li key={revision.number} className="flex flex-wrap items-center justify-between gap-2 text-sm">
              <span>版本 {revision.number} · {revision.document.widgets.length} 个 Widget{revision.number === selected.published?.number ? ' · 当前发布' : ''}</span>
              <div className="flex gap-2"><button type="button" disabled={historyLoading} aria-pressed={previewedHistoryRevision === revision.number} onClick={() => setPreviewedHistoryRevision(current => current === revision.number ? null : revision.number)} className="rounded-md border px-2 py-1 text-xs">{previewedHistoryRevision === revision.number ? '隐藏内容' : `查看版本 ${revision.number} 内容`}</button>
                {revision.number !== selected.published?.number && <button type="button" disabled={busy || historyLoading} onClick={() => void rollback(revision.number)} className="rounded-md border px-2 py-1 text-xs disabled:opacity-50">回滚到版本 {revision.number}</button>}</div>
            </li>)}</ul>
            {previewedHistory && <section aria-label={`版本 ${previewedHistory.number} 内容预览`} className="mt-3 rounded-md bg-muted/40 p-3 text-xs">
              <h5 className="font-medium">版本 {previewedHistory.number} 的 Widget</h5>
              {previewedHistory.document.widgets.length === 0 ? <p className="mt-2 text-muted-foreground">该版本没有 Widget。</p> : <ul className="mt-2 space-y-1">{previewedHistory.document.widgets.map(widget => <li key={widget.id}>{widget.title} · {widget.kind}{widget.descriptor_id ? ` · ${widget.descriptor_id}` : ''}{widget.plugin_metric ? ` · ${pluginMetricLabel(widget.plugin_metric)}` : ''}{widget.device_id ? ` · ${widget.device_id}` : ''}</li>)}</ul>}
              <p className="mt-2 text-muted-foreground">布局：{previewedHistory.document.layouts.map(layout => `${layout.breakpoint} ${layout.items.length} 项`).join('，') || '无'}</p>
            </section>}
          </section>}
          {canEdit && <div className="flex flex-wrap items-end gap-2"><label className="text-sm">设备<select aria-label="设备" value={currentDeviceID} onChange={event => { const value = event.target.value; setDeviceID(value); setPluginDescriptors([]); setPluginCatalogDeviceID(''); setPluginDescriptorID(''); setPluginCatalogMessage(value ? '正在读取已观测插件指标…' : '选择设备后读取插件指标。') }} className="mt-1 block rounded-md border bg-background px-3 py-2">{devices.map(device => <option key={device.snapshot.device.id} value={device.snapshot.device.id}>{device.snapshot.device.display_name || device.snapshot.device.id}</option>)}</select></label><label className="text-sm">来源<select aria-label="指标来源" value={metricSource} onChange={event => setMetricSource(event.target.value as 'builtin' | 'plugin')} className="mt-1 block rounded-md border bg-background px-3 py-2"><option value="builtin">内建指标</option><option value="plugin">插件指标</option></select></label>{metricSource === 'builtin' ? <label className="text-sm">指标<select aria-label="指标" value={descriptorID} onChange={event => setDescriptorID(event.target.value)} className="mt-1 block rounded-md border bg-background px-3 py-2">{descriptors.map(descriptor => <option key={descriptor.id} value={descriptor.id}>{descriptor.name} · {descriptor.unit}</option>)}</select></label> : <label className="text-sm">插件指标<select aria-label="插件指标" value={pluginDescriptorID} onChange={event => setPluginDescriptorID(event.target.value)} disabled={!availablePluginDescriptors.length} className="mt-1 block rounded-md border bg-background px-3 py-2">{availablePluginDescriptors.map(descriptor => <option key={pluginMetricKey(descriptor)} value={pluginMetricKey(descriptor)}>{descriptor.plugin_id}@{descriptor.plugin_version} · {descriptor.collector_id}/{descriptor.metric_id} · {descriptor.unit}</option>)}</select></label>}<button type="button" disabled={!canAddWidget} onClick={addWidget} className="rounded-md border px-3 py-2 text-sm">添加时间序列</button>{metricSource === 'plugin' && pluginCatalogMessage && <p role="status" className="w-full text-xs text-muted-foreground">{pluginCatalogMessage}</p>}</div>}
          {document.widgets.length === 0 ? <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">当前布局为空。</p> : <ul className="space-y-2">{document.widgets.map(widget => {
            const preview = previews[widget.id]
            return <li key={widget.id} className="rounded-lg border p-3"><div className="flex items-center justify-between gap-3"><span className="min-w-0 truncate text-sm">{widget.title}<span className="ml-2 text-xs text-muted-foreground">{widget.descriptor_id || (widget.plugin_metric && pluginMetricLabel(widget.plugin_metric))}</span></span><div className="flex shrink-0 gap-2">{!widget.plugin_metric && <button type="button" disabled={!widget.descriptor_id || !widget.device_id || preview?.loading} onClick={() => void previewWidget(widget)} className="rounded-md border px-2 py-1 text-xs disabled:opacity-50">{preview?.loading ? '读取中…' : '读取当前值'}</button>}{canEdit && <button type="button" onClick={() => removeWidget(widget.id)} className="rounded-md border px-2 py-1 text-xs">移除</button>}</div></div>
              {preview?.error && <p role="alert" className="mt-2 text-xs text-destructive">{preview.error}</p>}
              {preview?.result && <div className="mt-2 space-y-1 text-xs" aria-live="polite">{preview.result.points.map((point, index) => <p key={index} className="text-muted-foreground">{Object.entries(point.dimensions || {}).map(([key, value]) => `${key}: ${value}`).join(' · ')}{Object.keys(point.dimensions || {}).length ? ' · ' : ''}{point.value == null ? point.quality : `${point.value.toLocaleString()} ${preview.result!.descriptor.unit} · ${point.quality}`}</p>)}</div>}
              {widget.plugin_metric && widget.device_id && <PluginWidgetHistoryPreview widgetID={widget.id} deviceID={widget.device_id} reference={widget.plugin_metric} dark={dark}/>}
            </li>
          })}</ul>}
          <p className="text-xs text-muted-foreground">当前编辑只保存 Widget 引用和布局。内建手动预览只读 Server 最新快照；插件历史预览要求显式时间范围和点数上限。查询授权仍由 Server 执行，不会触发 Agent 采样。</p>
        </>}
      </div>
    </div>}
  </section>
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, { ...init, credentials: 'same-origin' })
  if (!response.ok) {
    const payload = await response.json().catch(() => undefined) as { error?: { message?: string; code?: string } } | undefined
    throw new Error(payload?.error?.message || `仪表盘服务返回 ${response.status}`)
  }
  if (response.status === 204) return undefined as T
  return await response.json() as T
}

function layoutEnd(items: Placement[]) {
  return items.reduce((end, item) => Math.max(end, item.y + item.height), 0)
}
