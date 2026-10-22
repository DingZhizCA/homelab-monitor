/**
 * 临时跨设备比较的数据整形。
 * 仅处理当前浏览器持有的样本，不访问 API 或改变设备采样。
 */
import type { DeviceState, Point } from '@/types'

export const exploreMetricOptions = [
  { id: 'cpu', label: 'CPU 使用率' },
  { id: 'memory', label: '内存使用率' },
  { id: 'rx', label: '接收速率' },
  { id: 'tx', label: '发送速率' },
] as const

export type ExploreMetric = typeof exploreMetricOptions[number]['id']

/** Build chart series from the current bounded browser history only. */
export function comparisonSeries(devices: DeviceState[], history: Record<string, Point[]>, metric: ExploreMetric) {
  return devices.flatMap(device => {
    const points = history[device.snapshot.device.id] || []
    if (points.length === 0) return []
    return [{
      name: `${device.snapshot.device.display_name} (${device.snapshot.device.id})`,
      type: 'line' as const,
      showSymbol: false,
      data: points.map(point => [point.time, point[metric]]),
    }]
  })
}
