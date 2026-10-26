/**
 * 当前 Session 用户的精确通知订阅编辑器。
 * 列表读取和每次变更都由用户显式触发，不创建角色默认订阅。
 */
import { useState } from 'react'
import { BellPlus, PencilLine } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { NotificationSubscription } from '@/types'

type Severity = NotificationSubscription['severity']
const emptyForm = () => ({ ruleID: '', severity: '' as Severity | '', enabled: '' as 'true' | 'false' | '', inApp: false, push: false })
type SubscriptionForm = ReturnType<typeof emptyForm>

export function NotificationSubscriptionManagement({ deviceID }: { deviceID: string }) {
  const [limit, setLimit] = useState('')
  const [subscriptions, setSubscriptions] = useState<NotificationSubscription[]>([])
  const [loaded, setLoaded] = useState(false)
  const [editing, setEditing] = useState<NotificationSubscription | null | undefined>(undefined)
  const [form, setForm] = useState<SubscriptionForm>(emptyForm)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')

  async function load() {
    const parsed = Number(limit)
    if (!Number.isInteger(parsed) || parsed < 1) {
      setMessage('请填写正整数订阅条数上限。')
      return
    }
    setBusy(true)
    setMessage('')
    try {
	  const result = await request<{ subscriptions: NotificationSubscription[] }>(`/api/v1/notifications/subscriptions?${new URLSearchParams({ device_id: deviceID, limit: String(parsed) })}`)
	  setSubscriptions(result.subscriptions)
      setLoaded(true)
    } catch (reason) {
      setMessage(reason instanceof Error ? reason.message : '无法读取通知订阅。')
    } finally { setBusy(false) }
  }

  function create() {
    setEditing(null)
    setForm(emptyForm())
    setMessage('')
  }

  function edit(subscription: NotificationSubscription) {
    setEditing(subscription)
    setForm({
      ruleID: subscription.rule_id, severity: subscription.severity,
      enabled: subscription.enabled ? 'true' : 'false',
      inApp: subscription.channels.in_app, push: subscription.channels.push,
    })
    setMessage('')
  }

  async function save(event: React.FormEvent) {
    event.preventDefault()
    if (!form.ruleID.trim() || form.ruleID.trim() !== form.ruleID || !form.severity || !form.enabled) {
      setMessage('请完整填写规则 ID、级别和启用状态。')
      return
    }
    if (form.enabled === 'true' && !form.inApp && !form.push) {
      setMessage('启用订阅时至少选择一个渠道。')
      return
    }
    if (form.severity === 'info' && form.push) {
      setMessage('Info 只能选择站内记录。')
      return
    }
    const next: NotificationSubscription = {
      user_id: editing?.user_id || '', device_id: deviceID, rule_id: form.ruleID,
      severity: form.severity, revision: editing ? editing.revision + 1 : 1,
      enabled: form.enabled === 'true', channels: { in_app: form.inApp, push: form.push },
    }
    setBusy(true)
    setMessage('')
    try {
      await request('/api/v1/notifications/subscriptions', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          expected_revision: editing?.revision || 0,
          subscription: {
            device_id: next.device_id, rule_id: next.rule_id, severity: next.severity,
            revision: next.revision, enabled: next.enabled, channels: next.channels,
          },
        }),
      })
      if (loaded) {
        setSubscriptions(current => [...current.filter(item => !(item.rule_id === next.rule_id && item.severity === next.severity)), next]
          .sort((a, b) => a.rule_id.localeCompare(b.rule_id) || a.severity.localeCompare(b.severity)))
      }
      setEditing(undefined)
      setForm(emptyForm())
      setMessage(`订阅 ${next.rule_id}/${severityLabel(next.severity)} Revision ${next.revision} 已保存。`)
    } catch (reason) {
      setMessage(reason instanceof Error ? reason.message : '无法保存通知订阅。')
    } finally { setBusy(false) }
  }

  return <Card className="mt-5">
    <CardHeader className="flex-row items-center justify-between gap-3"><div><CardTitle className="text-base">我的通知订阅</CardTitle><p className="mt-1 text-xs text-muted-foreground">精确绑定当前设备、规则和级别；系统不会按角色自动订阅。</p></div><Button type="button" size="sm" variant="outline" onClick={create}><BellPlus/>新建订阅</Button></CardHeader>
    <CardContent className="grid gap-4">
      <div className="flex flex-wrap items-end gap-2"><label className="grid gap-1 text-sm">订阅条数上限<input aria-label="订阅条数上限" type="number" min="1" step="1" value={limit} onChange={event => setLimit(event.target.value)} className="w-40 rounded-md border bg-background px-3 py-2"/></label><Button type="button" variant="outline" disabled={busy} onClick={() => void load()}>{busy ? '读取中…' : '读取我的订阅'}</Button></div>
      {message && <p role={message.startsWith('订阅 ') ? 'status' : 'alert'} className="rounded-lg border p-3 text-sm text-muted-foreground">{message}</p>}
      {loaded && (subscriptions.length ? <ul className="divide-y rounded-lg border">{subscriptions.map(subscription => <li key={`${subscription.rule_id}/${subscription.severity}`} className="flex flex-wrap items-center justify-between gap-3 p-3 text-sm"><div><div className="flex flex-wrap items-center gap-2"><b>{subscription.rule_id}</b><Badge variant="secondary">{severityLabel(subscription.severity)}</Badge><Badge variant="outline">Revision {subscription.revision}</Badge><span className="text-xs text-muted-foreground">{subscription.enabled ? '已启用' : '已停用'} · {channelLabel(subscription)}</span></div></div><Button type="button" size="sm" variant="outline" onClick={() => edit(subscription)}><PencilLine/>修订</Button></li>)}</ul> : <p className="text-sm text-muted-foreground">该设备还没有我的订阅。</p>)}
      {editing !== undefined && <form onSubmit={event => void save(event)} className="grid gap-3 rounded-lg border p-4"><h3 className="font-medium">{editing ? `修订 ${editing.rule_id}/${severityLabel(editing.severity)}` : '新建精确订阅'}</h3><div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        <label className="grid gap-1 text-sm">规则 ID<input aria-label="订阅规则 ID" required disabled={editing !== null} value={form.ruleID} onChange={event => setForm(current => ({ ...current, ruleID: event.target.value }))} className="rounded-md border bg-background px-3 py-2"/></label>
        <label className="grid gap-1 text-sm">严重级别<select aria-label="订阅严重级别" required disabled={editing !== null} value={form.severity} onChange={event => { const severity = event.target.value as SubscriptionForm['severity']; setForm(current => ({ ...current, severity, push: severity === 'info' ? false : current.push })) }} className="rounded-md border bg-background px-3 py-2"><option value="">请选择</option><option value="critical">Critical</option><option value="warning">Warning</option><option value="info">Info</option></select></label>
        <label className="grid gap-1 text-sm">启用状态<select aria-label="订阅启用状态" required value={form.enabled} onChange={event => setForm(current => ({ ...current, enabled: event.target.value as SubscriptionForm['enabled'] }))} className="rounded-md border bg-background px-3 py-2"><option value="">请选择</option><option value="true">启用</option><option value="false">停用</option></select></label>
      </div><div className="flex flex-wrap gap-4 text-sm"><label className="flex items-center gap-2"><input aria-label="站内通知渠道" type="checkbox" checked={form.inApp} onChange={event => setForm(current => ({ ...current, inApp: event.target.checked }))}/>站内通知</label><label className="flex items-center gap-2"><input aria-label="Push 渠道" type="checkbox" disabled={form.severity === 'info'} checked={form.push} onChange={event => setForm(current => ({ ...current, push: event.target.checked }))}/>Push</label></div><div className="flex gap-2"><Button type="submit" disabled={busy}>{busy ? '保存中…' : '保存新 Revision'}</Button><Button type="button" variant="outline" onClick={() => { setEditing(undefined); setForm(emptyForm()) }}>取消</Button></div></form>}
    </CardContent>
  </Card>
}

function severityLabel(severity: Severity) { return severity === 'critical' ? 'Critical' : severity === 'warning' ? 'Warning' : 'Info' }
function channelLabel(subscription: NotificationSubscription) { return [subscription.channels.in_app && '站内', subscription.channels.push && 'Push'].filter(Boolean).join(' + ') || '无渠道' }

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
