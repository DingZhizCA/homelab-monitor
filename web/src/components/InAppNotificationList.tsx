/**
 * 当前 Session 用户的站内通知列表。读取和已读变更都由用户显式触发。
 */
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { InAppNotification } from '@/types'

export function InAppNotificationList() {
  const [limit, setLimit] = useState('')
  const [items, setItems] = useState<InAppNotification[]>([])
  const [loaded, setLoaded] = useState(false)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)

  async function load() {
    const parsed = Number(limit)
    if (!Number.isInteger(parsed) || parsed < 1) { setMessage('请填写正整数通知条数上限。'); return }
    setBusy(true); setMessage('')
    try {
      const response = await fetch(`/api/v1/notifications?${new URLSearchParams({ limit: String(parsed) })}`, { credentials: 'same-origin' })
      if (!response.ok) throw new Error(`站内通知查询返回 ${response.status}`)
      const body = await response.json() as { notifications: InAppNotification[] }
      setItems(body.notifications); setLoaded(true)
    } catch (reason) { setMessage(reason instanceof Error ? reason.message : '无法读取站内通知。') } finally { setBusy(false) }
  }

  async function markRead(item: InAppNotification) {
    setBusy(true); setMessage('')
    try {
      const response = await fetch('/api/v1/notifications', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ notification_id: item.notification.id }) })
      if (!response.ok) throw new Error(`已读更新返回 ${response.status}`)
      setItems(current => current.map(candidate => candidate.notification.id === item.notification.id ? { ...candidate, read_at: new Date().toISOString() } : candidate))
    } catch (reason) { setMessage(reason instanceof Error ? reason.message : '无法更新已读状态。') } finally { setBusy(false) }
  }

  return <Card className="mt-5"><CardHeader><CardTitle className="text-base">我的站内通知</CardTitle><p className="mt-1 text-xs text-muted-foreground">按创建时间读取有限条记录，不自动轮询。</p></CardHeader><CardContent className="grid gap-4">
    <div className="flex flex-wrap items-end gap-2"><label className="grid gap-1 text-sm">通知条数上限<input aria-label="通知条数上限" type="number" min="1" step="1" value={limit} onChange={event => setLimit(event.target.value)} className="w-40 rounded-md border bg-background px-3 py-2"/></label><Button type="button" variant="outline" disabled={busy} onClick={() => void load()}>{busy ? '处理中…' : '读取我的通知'}</Button></div>
    {message && <p role="alert" className="rounded-lg border p-3 text-sm text-muted-foreground">{message}</p>}
    {loaded && (items.length ? <ul className="divide-y rounded-lg border">{items.map(item => <li key={item.notification.id} className="flex flex-wrap items-center justify-between gap-3 p-3 text-sm"><div><b>{item.notification.rule_id}</b><p className="text-xs text-muted-foreground">{item.notification.kind === 'firing' ? '触发' : '恢复'} · {item.notification.severity} · {new Date(item.notification.created_at).toLocaleString()}</p></div>{item.read_at ? <span className="text-xs text-muted-foreground">已读</span> : <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void markRead(item)}>标记已读</Button>}</li>)}</ul> : <p className="text-sm text-muted-foreground">还没有站内通知。</p>)}
  </CardContent></Card>
}
