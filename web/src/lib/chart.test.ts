import { describe, expect, it } from 'vitest'
import { chartTooltip, temperatureChartData } from './chart'
import type { Point } from '@/types'

const point = (time: number, temperatureTime: number, temperature: number): Point => ({ time, cpu: 1, memory: 2, rx: 3, tx: 4, temperature, temperature_time: temperatureTime })

describe('chart data', () => {
  it('deduplicates cached temperature and includes earlier sensor time in the domain', () => {
    const result = temperatureChartData([point(20_000, 10_000, 40), point(25_000, 10_000, 40)])
    expect(result.data).toEqual([[10_000, 40]])
    expect(result.min).toBeLessThan(10_000)
    expect(result.max).toBeGreaterThan(25_000)
  })

  it('formats tooltip units and precision', () => {
    const time = () => '12:00:00'
    expect(chartTooltip([{ axisValue: 1, seriesName: 'CPU 使用率', value: [1, 12.345] }], 'resource', time)).toContain('CPU 使用率：12.3%')
    expect(chartTooltip([{ axisValue: 1, seriesName: '接收速率', value: [1, 1536] }], 'network', time)).toContain('接收速率：1.5 KB/s')
  })
})
