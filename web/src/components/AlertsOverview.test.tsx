/**
 * Alerts page tests protect device scoping and the read-only current-state contract.
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AlertsOverview } from '@/components/AlertsOverview'
import type { DeviceState } from '@/types'

const device = (id: string): DeviceState => ({
  status: 'online', received_at: '2026-10-25T08:00:00Z',
  snapshot: {
    schema_version: 1, sampling_mode: 'idle', sampling_interval_seconds: 60,
    device: { id, display_name: id.toUpperCase(), hostname: id }, timestamp: '2026-10-25T08:00:00Z',
    system: { uptime_seconds: 1, load1: 0, load5: 0, load15: 0 },
    cpu: { usage_percent: 1, logical_cores: 1, frequency_mhz: null, temperature_celsius: null, sensor_collected_at: null },
    memory: { total_bytes: 1, used_bytes: 0, available_bytes: 1, usage_percent: 0 }, disks: [], networks: [],
  },
})

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('AlertsOverview', () => {
  it('loads current states for the selected device and refreshes only on request', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => ({
      ok: true, status: 200, json: async () => ({ incidents: [{
        device_id: new URL(String(input), 'https://monitor.example').searchParams.get('device_id'), rule_id: 'high-cpu', rule_revision: 2,
        status: 'firing', firing_since: '2026-10-25T07:55:00Z', last_evaluated_at: '2026-10-25T08:00:00Z', last_evaluation: 'true',
      }] }),
    }))
    vi.stubGlobal('fetch', fetchMock)
    render(<AlertsOverview devices={[device('node-a'), device('node-b')]} role="viewer"/>)

    expect(await screen.findByText('high-cpu')).toBeInTheDocument()
    expect(screen.getByText('告警中')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0][0])).toContain('device_id=node-a')

    fireEvent.change(screen.getByLabelText('告警设备'), { target: { value: 'node-b' } })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(String(fetchMock.mock.calls[1][0])).toContain('device_id=node-b')
    fireEvent.click(screen.getByRole('button', { name: '刷新' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
  })

  it('explains when evaluation current-state queries are unavailable', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 404 })))
    render(<AlertsOverview devices={[device('node-a')]} role="viewer"/>)
    expect(await screen.findByRole('alert')).toHaveTextContent('当前 Incident 查询未启用。')
  })

  it('does not query without a device', () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    render(<AlertsOverview devices={[]} role="viewer"/>)
    expect(screen.getByText('还没有可查询的设备。')).toBeInTheDocument()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
