import { render } from '@testing-library/react'
import type { EChartsOption } from 'echarts'
import { afterEach, describe, expect, it, vi } from 'vitest'

const { chart, init } = vi.hoisted(() => {
  const chart = { setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn() }
  return { chart, init: vi.fn(() => chart) }
})

vi.mock('echarts/core', () => ({ init, use: vi.fn() }))
vi.mock('echarts/charts', () => ({ LineChart: {} }))
vi.mock('echarts/components', () => ({ GridComponent: {}, LegendComponent: {}, TooltipComponent: {} }))
vi.mock('echarts/renderers', () => ({ CanvasRenderer: {} }))

import { MetricChart } from './MetricChart'

class TestResizeObserver implements ResizeObserver {
  observe = vi.fn()
  unobserve = vi.fn()
  disconnect = vi.fn()

  constructor(_callback: ResizeObserverCallback) {}
}

describe('MetricChart lifecycle', () => {
  afterEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('ResizeObserver', TestResizeObserver)
  })

  it('reuses one chart instance for option updates and disposes it on unmount', () => {
    vi.stubGlobal('ResizeObserver', TestResizeObserver)
    const firstOption = { series: [] } as EChartsOption
    const nextOption = { series: [{ type: 'line', data: [1, 2] }] } as EChartsOption
    const view = render(<MetricChart option={firstOption} label="CPU" />)

    view.rerender(<MetricChart option={nextOption} label="CPU" />)

    expect(init).toHaveBeenCalledTimes(1)
    expect(chart.setOption).toHaveBeenNthCalledWith(1, firstOption)
    expect(chart.setOption).toHaveBeenNthCalledWith(2, nextOption)
    view.unmount()
    expect(chart.dispose).toHaveBeenCalledOnce()
  })
})
