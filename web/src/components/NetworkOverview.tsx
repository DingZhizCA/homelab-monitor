/**
 * 已上报网络接口的只读汇总视图。
 * 数据来自现有 Agent 快照，不主动采集、不归因连接，也不展示持久化历史。
 */
import { Network } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatByteRate, formatBytes } from '@/lib/metrics'
import type { DeviceState } from '@/types'

export function NetworkOverview({ devices }: { devices: DeviceState[] }) {
  const rows = devices.flatMap(device => device.snapshot.networks.map(network => ({
    deviceID: device.snapshot.device.id,
    deviceName: device.snapshot.device.display_name,
    status: device.status,
    sampleTime: device.snapshot.timestamp,
    receivedAt: device.received_at,
    samplingInterval: device.snapshot.sampling_interval_seconds,
    network,
  })))

  return <section aria-labelledby="network-title" className="min-w-0">
    <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
      <div><h2 id="network-title" className="text-2xl font-semibold">网络接口</h2><p className="mt-1 text-sm text-muted-foreground">展示 Agent 最近上报的接口计数与速率，不代表按设备或出口归属的流量统计。</p></div>
      <Badge variant="outline">{rows.length} 个接口</Badge>
    </div>
    <Card>
      <CardHeader><CardTitle className="flex items-center gap-2 text-base"><Network size={18}/>接口观测</CardTitle></CardHeader>
      <CardContent>
        {rows.length === 0 ? <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">暂无接口数据。页面不会为了显示网络信息主动触发设备采集。</p> : <div className="overflow-x-auto">
          <table className="w-full min-w-[1080px] text-sm">
            <thead className="text-left text-xs text-muted-foreground"><tr className="border-b"><th className="pb-2">设备</th><th>接口</th><th>状态</th><th>采样周期</th><th>接收速率</th><th>发送速率</th><th>累计接收</th><th>累计发送</th><th>采样时间</th><th>Server 收到时间</th></tr></thead>
            <tbody>{rows.map(({ deviceID, deviceName, status, sampleTime, receivedAt, samplingInterval, network }) => <tr key={`${deviceID}:${network.interface}`} className="border-b last:border-0">
              <td className="py-3"><span className="font-medium">{deviceName}</span><span className="ml-2 text-xs text-muted-foreground">{deviceID}</span></td>
              <td className="font-medium">{network.interface}</td><td>{statusLabel(status)}</td>
              <td>{samplingInterval} 秒</td>
              <td>{formatByteRate(network.rx_bytes_per_second)}</td><td>{formatByteRate(network.tx_bytes_per_second)}</td>
              <td>{formatBytes(network.rx_bytes)}</td><td>{formatBytes(network.tx_bytes)}</td>
              <td>{formatTimestamp(sampleTime)}</td><td>{formatTimestamp(receivedAt)}</td>
            </tr>)}</tbody>
          </table>
        </div>}
        <p className="mt-3 text-xs text-muted-foreground">速率由 Agent 根据相邻计数器样本计算。MetricEnvelope v1 没有首样本或计数器重置质量标志，因此 0 速率不一定代表已确认无流量。</p>
      </CardContent>
    </Card>
  </section>
}

function formatTimestamp(value: string) {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZoneName: 'short',
  }).format(new Date(value))
}

function statusLabel(status: DeviceState['status']) {
  return status === 'online' ? '在线' : status === 'stale' ? '延迟' : '离线'
}
