/**
 * Owner 的版本化单指标告警规则编辑器。
 * 编辑器不生成阈值或策略默认值，也不会把已有 AND/OR 表达式降级为单指标规则。
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { History, PencilLine, Plus, RotateCcw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { IncidentArchive, IncidentState, MetricDescriptor } from '@/types'

type Severity = 'critical' | 'warning' | 'info'
type Operator = 'gt' | 'gte' | 'lt' | 'lte'
interface MetricComparison { descriptor_id: string; labels?: Record<string, string>; operator: Operator; threshold: number; unit: string }
interface Expression { metric?: MetricComparison; all?: Expression[]; any?: Expression[] }
interface AlertRule { id: string; device_id: string; revision: number; severity: Severity; enabled: boolean; expression: Expression }

const emptyForm = () => ({ ruleID: '', severity: '' as Severity | '', enabled: '' as 'true' | 'false' | '', descriptorID: '', operator: '' as Operator | '', threshold: '', labels: {} as Record<string, string> })
type RuleForm = ReturnType<typeof emptyForm>

export function AlertRuleManagement({ deviceID, incidents = [], onActivated }: { deviceID: string; incidents?: IncidentState[]; onActivated?: () => void }) {
  const [rules, setRules] = useState<AlertRule[]>([])
  const [descriptors, setDescriptors] = useState<MetricDescriptor[]>([])
  const [editingID, setEditingID] = useState<string | null>(null)
  const [form, setForm] = useState<RuleForm>(emptyForm)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [historyRuleID, setHistoryRuleID] = useState('')
  const [revisions, setRevisions] = useState<AlertRule[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const [historyMessage, setHistoryMessage] = useState('')
  const [archiveLimit, setArchiveLimit] = useState('')
  const [archives, setArchives] = useState<IncidentArchive[]>([])
  const [archiveLoading, setArchiveLoading] = useState(false)
  const [archiveLoaded, setArchiveLoaded] = useState(false)
  const [archiveMessage, setArchiveMessage] = useState('')
  const historyRequest = useRef<AbortController | null>(null)
  const archiveRequest = useRef<AbortController | null>(null)
  const selectedDescriptor = useMemo(() => descriptors.find(descriptor => descriptor.id === form.descriptorID), [descriptors, form.descriptorID])

  useEffect(() => {
    const controller = new AbortController()
    if (!deviceID) return () => controller.abort()
    Promise.all([
      request<{ rules: AlertRule[] }>(`/api/v1/owner/alert-rules?${new URLSearchParams({ device_id: deviceID })}`, { signal: controller.signal }),
      request<{ descriptors: MetricDescriptor[] }>('/api/v1/dashboard/metrics/descriptors', { signal: controller.signal }),
    ]).then(([ruleResult, descriptorResult]) => {
      setRules(ruleResult.rules)
      setDescriptors(descriptorResult.descriptors)
      setMessage('')
    }).catch(reason => {
      if (!(reason instanceof Error && reason.name === 'AbortError')) setMessage(reason instanceof Error ? reason.message : '无法读取告警规则。')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => { controller.abort(); historyRequest.current?.abort(); archiveRequest.current?.abort() }
  }, [deviceID])

  function createRule() {
    setEditingID('')
    setForm(emptyForm())
    setMessage('')
  }

  function editRule(rule: AlertRule) {
    if (!rule.expression.metric) return
    setEditingID(rule.id)
    setForm({
      ruleID: rule.id, severity: rule.severity, enabled: rule.enabled ? 'true' : 'false',
      descriptorID: rule.expression.metric.descriptor_id, operator: rule.expression.metric.operator,
      threshold: String(rule.expression.metric.threshold), labels: { ...rule.expression.metric.labels },
    })
    setMessage('')
  }

  function selectDescriptor(descriptorID: string) {
    const descriptor = descriptors.find(item => item.id === descriptorID)
    setForm(current => ({ ...current, descriptorID, labels: Object.fromEntries((descriptor?.dimensions || []).map(dimension => [dimension, current.labels[dimension] || ''])) }))
  }

  async function save(event: React.FormEvent) {
    event.preventDefault()
    const descriptor = selectedDescriptor
    const threshold = Number(form.threshold)
    const current = rules.find(rule => rule.id === editingID)
    if (!descriptor || !form.ruleID || !form.severity || !form.enabled || !form.operator || form.threshold.trim() === '' || !Number.isFinite(threshold)) {
      setMessage('请完整填写规则 ID、级别、启用状态、指标、运算符和有限阈值。')
      return
    }
    if ((descriptor.dimensions || []).some(dimension => form.labels[dimension] === undefined)) {
      setMessage('请为指标的每个维度填写选择值。')
      return
    }
    setBusy(true)
    setMessage('')
    const rule: AlertRule = {
      id: form.ruleID, device_id: deviceID, revision: current ? current.revision + 1 : 1,
      severity: form.severity, enabled: form.enabled === 'true',
      expression: { metric: { descriptor_id: descriptor.id, labels: { ...form.labels }, operator: form.operator, threshold, unit: descriptor.unit } },
    }
    try {
      await request<void>('/api/v1/owner/alert-rules', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ expected_current: current?.revision || 0, rule }),
      })
      setRules(existing => [...existing.filter(item => item.id !== rule.id), rule].sort((a, b) => a.id.localeCompare(b.id)))
      setEditingID(null)
      setForm(emptyForm())
      setMessage(`规则 ${rule.id} Revision ${rule.revision} 已保存。`)
      if (historyRuleID === rule.id) await loadHistory(rule.id)
    } catch (reason) {
      setMessage(reason instanceof Error ? reason.message : '无法保存告警规则。')
    } finally { setBusy(false) }
  }

  async function toggleHistory(rule: AlertRule) {
    if (historyRuleID === rule.id) {
      historyRequest.current?.abort()
      historyRequest.current = null
      archiveRequest.current?.abort()
      archiveRequest.current = null
      setHistoryRuleID('')
      setRevisions([])
      setHistoryMessage('')
      setArchiveLimit('')
      setArchives([])
      setArchiveLoaded(false)
      setArchiveMessage('')
      return
    }
    setHistoryRuleID(rule.id)
    await loadHistory(rule.id)
  }

  async function loadArchives() {
    const limit = Number(archiveLimit)
    if (!Number.isInteger(limit) || limit < 1) {
      setArchiveMessage('请填写正整数归档条数上限。')
      return
    }
    archiveRequest.current?.abort()
    const controller = new AbortController()
    archiveRequest.current = controller
    setArchiveLoading(true)
    setArchiveLoaded(false)
    setArchiveMessage('')
    const query = new URLSearchParams({ device_id: deviceID, rule_id: historyRuleID, limit: String(limit) })
    try {
      const result = await request<{ archives: IncidentArchive[] }>(`/api/v1/alerts/incidents/history?${query}`, { signal: controller.signal })
      if (!controller.signal.aborted) {
        setArchives(result.archives)
        setArchiveLoaded(true)
      }
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === 'AbortError')) setArchiveMessage(reason instanceof Error ? reason.message : '无法读取 Incident 归档。')
    } finally {
      if (archiveRequest.current === controller) {
        archiveRequest.current = null
        setArchiveLoading(false)
      }
    }
  }

  async function loadHistory(ruleID: string) {
    historyRequest.current?.abort()
    const controller = new AbortController()
    historyRequest.current = controller
    setHistoryLoading(true)
    setHistoryMessage('')
    const query = new URLSearchParams({ device_id: deviceID, rule_id: ruleID })
    try {
      const result = await request<{ revisions: AlertRule[] }>(`/api/v1/owner/alert-rules/revisions?${query}`, { signal: controller.signal })
      if (!controller.signal.aborted) setRevisions(result.revisions)
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === 'AbortError')) setHistoryMessage(reason instanceof Error ? reason.message : '无法读取规则历史。')
    } finally {
      if (historyRequest.current === controller) {
        historyRequest.current = null
        setHistoryLoading(false)
      }
    }
  }

  async function rollback(current: AlertRule, target: AlertRule) {
    setBusy(true)
    setHistoryMessage('')
    const newRevision = current.revision + 1
    try {
      await request<void>('/api/v1/owner/alert-rules/revisions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ device_id: deviceID, rule_id: current.id, target_revision: target.revision, new_revision: newRevision, expected_current: current.revision }),
      })
      const next = { ...target, revision: newRevision }
      setRules(existing => existing.map(rule => rule.id === current.id ? next : rule))
      setMessage(`规则 ${current.id} 已将 Revision ${target.revision} 重新发布为 Revision ${newRevision}。`)
      await loadHistory(current.id)
    } catch (reason) {
      setHistoryMessage(reason instanceof Error ? reason.message : '无法回滚告警规则。')
    } finally { setBusy(false) }
  }

  async function activateRevision(rule: AlertRule, incident: IncidentState) {
    setBusy(true)
    setMessage('')
    try {
      await request('/api/v1/owner/alerts/incidents/activate', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ device_id: deviceID, rule_id: rule.id, target_revision: rule.revision }),
      })
      setMessage(`规则 ${rule.id} Revision ${rule.revision} 已激活；旧 Revision ${incident.rule_revision} 已归档。`)
      onActivated?.()
    } catch (reason) {
      setMessage(reason instanceof Error ? reason.message : '无法激活规则 Revision。')
    } finally { setBusy(false) }
  }

  return <Card className="mt-5">
    <CardHeader className="flex-row items-center justify-between gap-3"><div><CardTitle className="text-base">规则管理</CardTitle><p className="mt-1 text-xs text-muted-foreground">Owner 可创建或修订单指标规则；复杂表达式保持只读。</p></div><Button type="button" size="sm" variant="outline" onClick={createRule}><Plus/>新建规则</Button></CardHeader>
    <CardContent className="grid gap-4">
      {message && <p role={message.startsWith('规则 ') ? 'status' : 'alert'} className="rounded-lg border p-3 text-sm text-muted-foreground">{message}</p>}
      {loading ? <p role="status" className="text-sm text-muted-foreground">正在读取规则…</p> : rules.length === 0 ? <p className="text-sm text-muted-foreground">该设备还没有规则。</p> : <ul className="divide-y rounded-lg border">{rules.map(rule => { const incident = incidents.find(item => item.rule_id === rule.id); const needsActivation = incident && incident.rule_revision !== rule.revision; return <li key={rule.id} className="flex flex-wrap items-center justify-between gap-3 p-3 text-sm"><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2"><b>{rule.id}</b><Badge variant="outline">Revision {rule.revision}</Badge><Badge variant="secondary">{severityLabel(rule.severity)}</Badge><span className="text-xs text-muted-foreground">{rule.enabled ? '已启用' : '已停用'}</span>{needsActivation && <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-300">等待激活</Badge>}</div><p className="mt-1 break-words text-xs text-muted-foreground">{describeExpression(rule.expression)}</p>{needsActivation && <p className="mt-1 text-xs text-amber-700 dark:text-amber-300">当前 Incident 属于 Revision {incident.rule_revision}，评估已暂停。</p>}</div><div className="flex flex-wrap items-center gap-2">{needsActivation && <Button type="button" size="sm" disabled={busy} onClick={() => void activateRevision(rule, incident)}>归档 Revision {incident.rule_revision} 并激活 Revision {rule.revision}</Button>}{rule.expression.metric ? <Button type="button" size="sm" variant="outline" onClick={() => editRule(rule)}><PencilLine/>修订</Button> : <span className="text-xs text-muted-foreground">复杂表达式只读</span>}<Button type="button" size="sm" variant="outline" onClick={() => void toggleHistory(rule)}><History/>{historyRuleID === rule.id ? '收起历史' : '历史'}</Button></div></li> })}</ul>}
      {historyRuleID && <div className="grid gap-3 rounded-lg border p-4"><h3 className="font-medium">{historyRuleID} 的不可变历史</h3>{historyMessage && <p role="alert" className="text-sm text-destructive">{historyMessage}</p>}{historyLoading ? <p role="status" className="text-sm text-muted-foreground">正在读取规则历史…</p> : revisions.length === 0 ? <p className="text-sm text-muted-foreground">暂无历史 Revision。</p> : <ul className="divide-y">{revisions.map(revision => { const current = rules.find(rule => rule.id === historyRuleID); return <li key={revision.revision} className="flex flex-wrap items-center justify-between gap-3 py-3 text-sm"><div><div className="flex items-center gap-2"><b>Revision {revision.revision}</b>{current?.revision === revision.revision && <Badge variant="secondary">当前</Badge>}<span className="text-xs text-muted-foreground">{severityLabel(revision.severity)} · {revision.enabled ? '已启用' : '已停用'}</span></div><p className="mt-1 break-words text-xs text-muted-foreground">{describeExpression(revision.expression)}</p></div>{current && current.revision !== revision.revision && <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void rollback(current, revision)}><RotateCcw/>以 Revision {revision.revision} 创建 Revision {current.revision + 1}</Button>}</li> })}</ul>}
        <div className="grid gap-3 border-t pt-3"><div><h4 className="text-sm font-medium">Superseded Incident 归档</h4><p className="mt-1 text-xs text-muted-foreground">明确填写本次最多读取的条数；查询不会评估规则或触发采集。</p></div><div className="flex flex-wrap items-end gap-2"><label className="grid gap-1 text-sm">归档条数上限<input aria-label="归档条数上限" type="number" min="1" step="1" value={archiveLimit} onChange={event => setArchiveLimit(event.target.value)} className="w-40 rounded-md border bg-background px-3 py-2"/></label><Button type="button" size="sm" variant="outline" disabled={archiveLoading} onClick={() => void loadArchives()}>{archiveLoading ? '读取中…' : '读取 Incident 归档'}</Button></div>{archiveMessage && <p role="alert" className="text-sm text-destructive">{archiveMessage}</p>}{archives.length > 0 && <ul className="divide-y rounded-lg border px-3">{archives.map(archive => <li key={archive.state.rule_revision} className="py-3 text-sm"><div className="flex flex-wrap items-center gap-2"><b>Revision {archive.state.rule_revision}</b><Badge variant="outline">{archive.state.status}</Badge><span className="text-xs text-muted-foreground">由 Revision {archive.superseded_by_revision} 替代</span></div><p className="mt-1 text-xs text-muted-foreground">归档时间 {archive.superseded_at} · 最后评估 {archive.state.last_evaluated_at}</p></li>)}</ul>}{archiveLoaded && archives.length === 0 && <p className="text-sm text-muted-foreground">该范围内没有 superseded Incident 归档。</p>}</div>
      </div>}
      {editingID !== null && <form onSubmit={event => void save(event)} className="grid gap-3 rounded-lg border p-4"><h3 className="font-medium">{editingID ? `修订 ${editingID}` : '新建单指标规则'}</h3><div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        <label className="grid gap-1 text-sm">规则 ID<input aria-label="规则 ID" required disabled={editingID !== ''} value={form.ruleID} onChange={event => setForm(current => ({ ...current, ruleID: event.target.value }))} className="rounded-md border bg-background px-3 py-2"/></label>
        <label className="grid gap-1 text-sm">严重级别<select aria-label="严重级别" required value={form.severity} onChange={event => setForm(current => ({ ...current, severity: event.target.value as RuleForm['severity'] }))} className="rounded-md border bg-background px-3 py-2"><option value="">请选择</option><option value="critical">Critical</option><option value="warning">Warning</option><option value="info">Info</option></select></label>
        <label className="grid gap-1 text-sm">启用状态<select aria-label="启用状态" required value={form.enabled} onChange={event => setForm(current => ({ ...current, enabled: event.target.value as RuleForm['enabled'] }))} className="rounded-md border bg-background px-3 py-2"><option value="">请选择</option><option value="true">启用</option><option value="false">停用</option></select></label>
        <label className="grid gap-1 text-sm">指标<select aria-label="规则指标" required value={form.descriptorID} onChange={event => selectDescriptor(event.target.value)} className="rounded-md border bg-background px-3 py-2"><option value="">请选择</option>{descriptors.map(descriptor => <option key={descriptor.id} value={descriptor.id}>{descriptor.name} · {descriptor.unit}</option>)}</select></label>
        <label className="grid gap-1 text-sm">运算符<select aria-label="规则运算符" required value={form.operator} onChange={event => setForm(current => ({ ...current, operator: event.target.value as RuleForm['operator'] }))} className="rounded-md border bg-background px-3 py-2"><option value="">请选择</option><option value="gt">大于</option><option value="gte">大于等于</option><option value="lt">小于</option><option value="lte">小于等于</option></select></label>
        <label className="grid gap-1 text-sm">阈值{selectedDescriptor ? `（${selectedDescriptor.unit}）` : ''}<input aria-label="规则阈值" required type="number" step="any" value={form.threshold} onChange={event => setForm(current => ({ ...current, threshold: event.target.value }))} className="rounded-md border bg-background px-3 py-2"/></label>
        {(selectedDescriptor?.dimensions || []).map(dimension => <label key={dimension} className="grid gap-1 text-sm">维度：{dimension}<input required aria-label={`规则维度 ${dimension}`} value={form.labels[dimension] || ''} onChange={event => setForm(current => ({ ...current, labels: { ...current.labels, [dimension]: event.target.value } }))} className="rounded-md border bg-background px-3 py-2"/></label>)}
      </div><div className="flex gap-2"><Button type="submit" disabled={busy}>{busy ? '保存中…' : '保存新 Revision'}</Button><Button type="button" variant="outline" onClick={() => { setEditingID(null); setForm(emptyForm()) }}>取消</Button></div></form>}
    </CardContent>
  </Card>
}

function severityLabel(severity: Severity) { return severity === 'critical' ? 'Critical' : severity === 'warning' ? 'Warning' : 'Info' }

function describeExpression(expression: Expression): string {
  if (expression.metric) {
    const labels = Object.entries(expression.metric.labels || {}).map(([key, value]) => `${key}=${value}`).join(', ')
    return `${expression.metric.descriptor_id}${labels ? ` {${labels}}` : ''} ${expression.metric.operator} ${expression.metric.threshold} ${expression.metric.unit}`
  }
  const children = expression.all || expression.any
  const joiner = expression.all ? ' AND ' : ' OR '
  return children?.length ? `(${children.map(describeExpression).join(joiner)})` : '无法识别的表达式'
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin', ...init })
  if (!response.ok) {
    let message = `请求失败（${response.status}）`
    try { const body = await response.json() as { error?: { message?: string } }; if (body.error?.message) message = body.error.message } catch { /* keep status */ }
    throw new Error(message)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
