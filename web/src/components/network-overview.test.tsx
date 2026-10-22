/**
 * Network overview tests for truthful rendering of existing Agent snapshots.
 * These checks guard against accidental collection and flow-attribution claims.
 */
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { AppNavigation } from '@/components/AppNavigation'
import { NetworkOverview } from '@/components/NetworkOverview'
import { comparisonSeries } from '@/lib/explore'
import type { DeviceState } from '@/types'

const device: DeviceState = {
  received_at: '2026-10-17T10:00:00Z',
  status: 'online',
  snapshot: {
    schema_version: 1,
    sampling_mode: 'idle',
    sampling_interval_seconds: 60,
    device: { id: 'node-a', display_name: 'Node A', hostname: 'node-a.local' },
    timestamp: '2026-10-17T10:00:00Z',
    system: { uptime_seconds: 3600, load1: 0, load5: 0, load15: 0 },
    cpu: { usage_percent: 0, logical_cores: 1, frequency_mhz: null, temperature_celsius: null, sensor_collected_at: null },
    memory: { total_bytes: 0, used_bytes: 0, available_bytes: 0, usage_percent: 0 },
    disks: [],
    networks: [{ interface: 'eth0', rx_bytes: 2048, tx_bytes: 1024, rx_bytes_per_second: 128, tx_bytes_per_second: 64 }],
  },
}

describe('Network overview', () => {
  it('renders device-scoped interface counters without presenting flow attribution', () => {
    render(<NetworkOverview devices={[device]}/>)

    expect(screen.getByRole('heading', { name: '网络接口' })).toBeInTheDocument()
    expect(screen.getByText('node-a')).toBeInTheDocument()
    expect(screen.getByText('eth0')).toBeInTheDocument()
    expect(screen.getByText(/不代表按设备或出口归属的流量统计/)).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '采样周期' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Server 收到时间' })).toBeInTheDocument()
    expect(screen.getByText('60 秒')).toBeInTheDocument()
    expect(screen.getByText(/0 速率不一定代表已确认无流量/)).toBeInTheDocument()
    expect(screen.queryByText(/Tailnet|Exit Node|互联网流量/)).not.toBeInTheDocument()
  })

  it('shows an explicit empty state without requesting collection', () => {
    render(<NetworkOverview devices={[]}/>)
    expect(screen.getByText(/不会为了显示网络信息主动触发设备采集/)).toBeInTheDocument()
  })
})

describe('App navigation', () => {
  it('marks the active view and reports page selection', () => {
    const onSelect = vi.fn()
    render(<AppNavigation current="dashboard" onSelect={onSelect}/>)

    expect(screen.getByRole('button', { name: '概览' })).toHaveAttribute('aria-current', 'page')
    fireEvent.click(screen.getByRole('button', { name: '网络接口' }))
    expect(onSelect).toHaveBeenCalledWith('network')
  })

  it('shows account administration only when the caller has Owner role', () => {
    const onSelect = vi.fn()
    const { rerender } = render(<AppNavigation current="dashboard" onSelect={onSelect}/>);
    expect(screen.queryByRole('button', { name: '账户' })).not.toBeInTheDocument()
    rerender(<AppNavigation current="dashboard" onSelect={onSelect} showOwnerPages/>);
    fireEvent.click(screen.getByRole('button', { name: '账户' }))
    expect(onSelect).toHaveBeenCalledWith('settings')
  })
})

describe('Temporary cross-device comparison', () => {
  it('uses only supplied browser history and keeps labels scoped to each device', () => {
    const points = [{ time: 1, cpu: 23, memory: 42, rx: 10, tx: 5, temperature: null, temperature_time: null }]
    const devices = [{ ...device, snapshot: { ...device.snapshot, device: { ...device.snapshot.device, display_name: '<img src=x>' } } }]
    const series = comparisonSeries(devices, { 'node-a': points }, 'cpu')

    expect(series).toHaveLength(1)
    expect(series[0].name).toBe('<img src=x> (node-a)')
    expect(series[0].data).toEqual([[1, 23]])
    expect(comparisonSeries(devices, {}, 'cpu')).toEqual([])
  })
})
