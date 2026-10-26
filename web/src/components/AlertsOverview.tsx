/**
 * 认证用户的当前告警状态视图。
 * 页面只读取 Server 已持久化的 Incident 当前状态，不轮询、不确认告警，也不触发采集。
 */
import { useEffect, useState } from 'react'
import { BellRing, RefreshCw } from 'lucide-react'
import { AlertRuleManagement } from '@/components/AlertRuleManagement'
import { NotificationSubscriptionManagement } from '@/components/NotificationSubscriptionManagement'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { DeviceState, IncidentState } from '@/types'

const statusLabel: Record<IncidentState['status'], string> = {
  inactive: '未触发', pending: '待触发', firing: '告警中', recovering: '恢复确认中', resolved: '已恢复',
}

const evaluationLabel: Record<IncidentState['last_evaluation'], string> = {
  true: '条件成立', false: '条件不成立', unknown: '数据未知', disabled: '规则已停用',
}

const statusStyle: Record<IncidentState['status'], string> = {
  inactive: 'bg-slate-500/10 text-slate-700 dark:text-slate-300',
  pending: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
  firing: 'bg-red-500/15 text-red-700 dark:text-red-300',
  recovering: 'bg-blue-500/15 text-blue-700 dark:text-blue-300',
  resolved: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
}

export function AlertsOverview({ devices, role }: { devices: DeviceState[]; role: 'owner' | 'viewer' }) {
  const [deviceID, setDeviceID] = useState('')
  const [incidents, setIncidents] = useState<IncidentState[]>([])
  const [loading, setLoading] = useState(false)
  const [message, setMessage] = useState('')
  const activeDeviceID = devices.some(device => device.snapshot.device.id === deviceID)
    ? deviceID
    : devices[0]?.snapshot.device.id || ''

  useEffect(() => {
    if (!activeDeviceID) return
    const controller = new AbortController()
    void load(activeDeviceID, controller.signal)
    return () => controller.abort()
  }, [activeDeviceID])

  async function load(selectedDeviceID: string, signal?: AbortSignal) {
    setLoading(true)
    setMessage('')
    const query = new URLSearchParams({ device_id: selectedDeviceID })
    try {
      const response = await fetch(`/api/v1/alerts/incidents?${query}`, { credentials: 'same-origin', signal })
      if (response.status === 404) throw new Error('当前 Incident 查询未启用。')
      if (!response.ok) throw new Error(`当前 Incident 查询返回 ${response.status}`)
      const result = await response.json() as { incidents: IncidentState[] }
      setIncidents(result.incidents)
      setMessage(result.incidents.length ? '' : '该设备还没有 Incident 当前状态。')
    } catch (reason) {
      if (reason instanceof Error && reason.name === 'AbortError') return
      setIncidents([])
      setMessage(reason instanceof Error ? reason.message : '无法读取当前 Incident 状态。')
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }

  return <section className="min-w-0">
    <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
      <div><h2 className="text-2xl font-semibold">告警</h2><p className="mt-1 text-sm text-muted-foreground">每条记录是一个规则 Revision 的当前转换状态，不是告警历史。</p></div>
      <div className="flex items-end gap-2">
        <label className="grid gap-1 text-sm">设备<select aria-label="告警设备" value={activeDeviceID} onChange={event => setDeviceID(event.target.value)} disabled={!devices.length} className="rounded-md border bg-background px-3 py-2"><option value="">请选择设备</option>{devices.map(device => <option key={device.snapshot.device.id} value={device.snapshot.device.id}>{device.snapshot.device.display_name || device.snapshot.device.id}</option>)}</select></label>
        <Button type="button" variant="outline" disabled={!activeDeviceID || loading} onClick={() => void load(activeDeviceID)}><RefreshCw/>刷新</Button>
      </div>
    </div>
    {!activeDeviceID && <p role="status" className="mb-4 rounded-lg border p-3 text-sm text-muted-foreground">还没有可查询的设备。</p>}
    {activeDeviceID && message && <p role={message.startsWith('无法') || message.includes('未启用') || message.includes('返回') ? 'alert' : 'status'} className="mb-4 rounded-lg border p-3 text-sm text-muted-foreground">{message}</p>}
    {loading ? <p role="status" className="text-sm text-muted-foreground">正在读取当前 Incident 状态…</p> : activeDeviceID && incidents.length > 0 && <div className="grid gap-3 lg:grid-cols-2">{incidents.map(incident => <Card key={`${incident.device_id}/${incident.rule_id}`}>
      <CardHeader className="flex-row items-start justify-between gap-3"><div><CardTitle className="flex items-center gap-2 text-base"><BellRing size={18}/>{incident.rule_id}</CardTitle><p className="mt-1 text-xs text-muted-foreground">Revision {incident.rule_revision}</p></div><Badge className={statusStyle[incident.status]}>{statusLabel[incident.status]}</Badge></CardHeader>
      <CardContent className="grid gap-2 text-sm"><div className="flex justify-between gap-3"><span className="text-muted-foreground">最近评估</span><span>{evaluationLabel[incident.last_evaluation]}</span></div><div className="flex justify-between gap-3"><span className="text-muted-foreground">评估时间</span><time dateTime={incident.last_evaluated_at}>{formatTime(incident.last_evaluated_at)}</time></div>{transitionTime(incident) && <div className="flex justify-between gap-3"><span className="text-muted-foreground">状态起始</span><time dateTime={transitionTime(incident)}>{formatTime(transitionTime(incident))}</time></div>}</CardContent>
    </Card>)}</div>}
    {role === 'owner' && activeDeviceID && <AlertRuleManagement key={activeDeviceID} deviceID={activeDeviceID} incidents={incidents} onActivated={() => void load(activeDeviceID)}/>}
    {activeDeviceID && <NotificationSubscriptionManagement key={`subscriptions/${activeDeviceID}`} deviceID={activeDeviceID}/>}
  </section>
}

function transitionTime(incident: IncidentState) {
  switch (incident.status) {
    case 'pending': return incident.pending_since
    case 'firing': return incident.firing_since
    case 'recovering': return incident.recovering_since
    case 'resolved': return incident.resolved_at
    default: return undefined
  }
}

function formatTime(value?: string) {
  if (!value || value.startsWith('0001-')) return '暂无数据'
  const parsed = new Date(value)
  return Number.isNaN(parsed.valueOf()) ? value : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(parsed)
}
