/**
 * Historical Metric Explorer tests for explicit query submission and optional
 * API availability.
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceState } from '@/types'

vi.mock('@/components/MetricChart', async () => {
  const React = await import('react')
  return {
    MetricChart: ({ label, option }: { label: string; option: { series?: unknown } }) => React.createElement('div', {
      role: 'img',
      'aria-label': label,
      'data-series': JSON.stringify(option.series),
    }),
  }
})

import { HistoricalMetricExplorer } from './HistoricalMetricExplorer'

const devices = [{ snapshot: { device: { id: 'node-a' } } }] as DeviceState[]
const descriptor = { id: 'cpu.usage_percent', name: 'CPU usage', unit: 'percent', description: 'CPU usage.' }
const history = {
  descriptor,
  device_id: 'node-a',
  from: '2026-10-18T10:00:00Z',
  to: '2026-10-18T11:00:00Z',
  points: [
    { device_id: 'node-a', descriptor_id: descriptor.id, sampled_at: '2026-10-18T10:10:00Z', value: 25, quality: 'valid' },
    { device_id: 'node-a', descriptor_id: descriptor.id, sampled_at: '2026-10-18T10:20:00Z', quality: 'missing' },
  ],
}

describe('HistoricalMetricExplorer', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/v1/dashboard/metrics/descriptors') {
        return { ok: true, status: 200, json: async () => ({ descriptors: [descriptor] }) }
      }
      return { ok: true, status: 200, json: async () => history }
    }))
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('loads descriptors but requests history only after explicit bounded form submission', async () => {
    render(<HistoricalMetricExplorer devices={devices} dark={false}/>)
    await waitFor(() => expect(screen.getByLabelText('历史指标')).toHaveValue(descriptor.id))
    const fetcher = vi.mocked(fetch)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(String(fetcher.mock.calls[0][0])).toBe('/api/v1/dashboard/metrics/descriptors')

    fireEvent.change(screen.getByLabelText('历史设备'), { target: { value: 'node-a' } })
    fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-10-18T10:00' } })
    fireEvent.change(screen.getByLabelText('结束时间'), { target: { value: '2026-10-18T11:00' } })
    fireEvent.change(screen.getByLabelText('最大点数'), { target: { value: '120' } })
    fireEvent.click(screen.getByRole('button', { name: '查询历史' }))

    await screen.findByRole('img', { name: 'CPU usage历史趋势' })
    const requestPath = String(fetcher.mock.calls[1][0])
    expect(requestPath).toContain('/api/v1/dashboard/metrics/history?')
    const query = new URL(requestPath, 'https://monitor.example').searchParams
    expect(query.get('descriptor_id')).toBe(descriptor.id)
    expect(query.get('device_id')).toBe('node-a')
    expect(query.get('limit')).toBe('120')
    expect(screen.getByText(/有效 1，缺失 1/)).toBeInTheDocument()
    const series = JSON.parse(screen.getByRole('img', { name: 'CPU usage历史趋势' }).getAttribute('data-series') || '[]')
    expect(series[0].connectNulls).toBe(false)
    expect(series[0].data[1][1]).toBeNull()
  })

  it('shows when the optional descriptor and history APIs are not configured', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 404, json: async () => ({}) })))
    render(<HistoricalMetricExplorer devices={devices} dark={false}/>)
    expect(await screen.findByRole('status')).toHaveTextContent('Server 尚未启用指标目录接口。')
    expect(screen.getByRole('button', { name: '查询历史' })).toBeDisabled()
  })
})
