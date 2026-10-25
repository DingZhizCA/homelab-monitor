/** Plugin history Explorer tests exact selectors and typed aggregate metadata. */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceState } from '@/types'

vi.mock('@/components/MetricChart', async () => {
  const React = await import('react')
  return { MetricChart: ({ label, option }: { label: string; option: { series?: unknown } }) => React.createElement('div', { role: 'img', 'aria-label': label, 'data-series': JSON.stringify(option.series) }) }
})

import { PluginHistoricalMetricExplorer } from './PluginHistoricalMetricExplorer'

const devices = [{ snapshot: { device: { id: 'node-a' } } }] as DeviceState[]
const descriptor = { device_id: 'node-a', plugin_id: 'smart', plugin_version: '1.0.0', collector_id: 'temperature', metric_id: 'temperature', type: 'gauge', unit: 'celsius', label_keys: ['disk'], first_observed_at: '2026-10-18T09:00:00Z', last_observed_at: '2026-10-18T11:00:00Z' }

describe('PluginHistoricalMetricExplorer', () => {
  beforeEach(() => vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path.startsWith('/api/v1/dashboard/plugin-metrics/descriptors?')) return { ok: true, status: 200, json: async () => ({ descriptors: [descriptor] }) }
    return { ok: true, status: 200, json: async () => ({ device_id: 'node-a', plugin_id: 'smart', plugin_version: '1.0.0', collector_id: 'temperature', metric_id: 'temperature', from: '2026-10-18T10:00:00Z', to: '2026-10-18T11:00:00Z', points: [{ observed_at: '2026-10-18T10:00:00Z', type: 'gauge', unit: 'celsius', summary: { mean: 42, min: 40, max: 44, last: 43, sample_count: 3 }, quality: 'partial', labels: { disk: 'sda' }, resolution_seconds: 60, aggregation_kind: 'gauge_summary' }] }) }
  })))

  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('queries one exact versioned descriptor and displays aggregate semantics', async () => {
    render(<PluginHistoricalMetricExplorer devices={devices} dark={false}/>)
    await waitFor(() => expect((screen.getByLabelText('插件历史指标') as HTMLSelectElement).value).toContain('smart'))
    fireEvent.change(screen.getByLabelText('插件开始时间'), { target: { value: '2026-10-18T10:00' } })
    fireEvent.change(screen.getByLabelText('插件结束时间'), { target: { value: '2026-10-18T11:00' } })
    fireEvent.change(screen.getByLabelText('插件最大点数'), { target: { value: '50' } })
    fireEvent.click(screen.getByRole('button', { name: '查询插件历史' }))
    expect(await screen.findByText(/分辨率：60 秒/)).toHaveTextContent('部分')
    expect(screen.getByText(/gauge_summary/)).toHaveTextContent('均值 42，最小 40，最大 44，最后 43')
    expect(await screen.findByRole('img', { name: 'smart temperature插件历史趋势' })).toBeInTheDocument()
    const calls = vi.mocked(fetch).mock.calls
    const query = new URL(String(calls[1][0]), 'https://monitor.example').searchParams
    expect(query.get('plugin_version')).toBe('1.0.0')
    expect(query.get('collector_id')).toBe('temperature')
    expect(query.get('metric_id')).toBe('temperature')
  })

  it('loads the first device catalog when devices arrive after mounting', async () => {
    const view = render(<PluginHistoricalMetricExplorer devices={[]} dark={false}/>)
    expect(fetch).not.toHaveBeenCalled()
    view.rerender(<PluginHistoricalMetricExplorer devices={devices} dark={false}/>)
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('device_id=node-a'), expect.objectContaining({ credentials: 'same-origin' })))
    expect(screen.getByLabelText('插件历史设备')).toHaveValue('node-a')
  })
})
