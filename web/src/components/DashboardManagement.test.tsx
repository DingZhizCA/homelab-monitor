/**
 * Dashboard editor tests verify role-specific actions and the JSON API contract.
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DashboardManagement } from '@/components/DashboardManagement'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('DashboardManagement', () => {
  it('creates and saves a personal layout using descriptor references', async () => {
    const calls: Array<{ path: string; init: RequestInit }> = []
    const descriptor = { id: 'cpu.usage_percent', name: 'CPU usage', unit: 'percent' }
    const devices = [{ snapshot: { device: { id: 'node-a', display_name: 'Node A' } } }]
    vi.stubGlobal('crypto', { randomUUID: () => 'widget-1' })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      calls.push({ path, init })
      if (path === '/api/v1/dashboards' && !init.method) return response({ dashboards: [] })
      if (path === '/api/v1/dashboard/metrics/descriptors') return response({ descriptors: [descriptor] })
      if (path.startsWith('/api/v1/dashboard/plugin-metrics/descriptors?')) return response({ descriptors: [] })
      if (path === '/api/v1/dashboards' && init.method === 'POST') {
        const body = JSON.parse(String(init.body))
        return response({ id: 'dash-1', name: body.name, scope: body.scope, draft: { number: 1, document: body.document } }, 201)
      }
      if (path === '/api/v1/dashboards/dash-1/personal' && init.method === 'PUT') {
        const body = JSON.parse(String(init.body))
        return response({ id: 'dash-1', name: 'Lab', scope: 'personal', draft: { number: 2, document: body.document } })
      }
      if (path.startsWith('/api/v1/dashboard/metrics/latest?')) return response({
        descriptor, device_id: 'node-a', sampled_at: '2026-10-18T01:00:00Z',
        points: [{ value: 23.5, quality: 'fresh' }],
      })
      throw new Error(`unexpected request ${path}`)
    }))

    render(<DashboardManagement role="viewer" devices={devices}/>)
    await screen.findByText('暂无仪表盘')
    fireEvent.change(screen.getByLabelText('仪表盘名称'), { target: { value: 'Lab' } })
    fireEvent.click(screen.getByRole('button', { name: '新建个人仪表盘' }))
    expect(await screen.findByRole('heading', { name: 'Lab' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '添加时间序列' }))
    expect(screen.getByText('CPU usage')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '保存布局' }))

    await waitFor(() => expect(calls.some(call => call.path.endsWith('/personal') && call.init.method === 'PUT')).toBe(true))
    const saveCall = calls.find(call => call.path.endsWith('/personal'))!
    const saved = JSON.parse(String(saveCall.init.body))
    expect(saved.expected_revision).toBe(1)
    expect(saved.document.widgets).toEqual([expect.objectContaining({ id: 'widget-1', descriptor_id: descriptor.id, device_id: 'node-a' })])
    expect(saved.document.layouts).toHaveLength(2)
    expect(saveCall.init.credentials).toBe('same-origin')
    fireEvent.click(screen.getByRole('button', { name: '读取当前值' }))
    expect(await screen.findByText('23.5 percent · fresh')).toBeInTheDocument()
    expect(calls.some(call => call.path.includes('descriptor_id=cpu.usage_percent') && call.path.includes('device_id=node-a'))).toBe(true)
  })

  it('saves an exact versioned plugin metric reference without inventing a preview range', async () => {
    const calls: Array<{ path: string; init: RequestInit }> = []
    const devices = [{ snapshot: { device: { id: 'node-a', display_name: 'Node A' } } }]
    const pluginDescriptor = { device_id: 'node-a', plugin_id: 'smart', plugin_version: '1.0.0', collector_id: 'health', metric_id: 'temperature', type: 'gauge', unit: 'celsius', label_keys: ['disk'], first_observed_at: '2026-10-25T00:00:00Z', last_observed_at: '2026-10-25T01:00:00Z' }
    vi.stubGlobal('crypto', { randomUUID: () => 'plugin-widget-1' })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      calls.push({ path, init })
      if (path === '/api/v1/dashboards' && !init.method) return response({ dashboards: [] })
      if (path === '/api/v1/dashboard/metrics/descriptors') return response({ descriptors: [] })
      if (path.startsWith('/api/v1/dashboard/plugin-metrics/descriptors?')) return response({ descriptors: [pluginDescriptor] })
      if (path === '/api/v1/dashboards' && init.method === 'POST') {
        const body = JSON.parse(String(init.body))
        return response({ id: 'dash-plugin', name: body.name, scope: body.scope, draft: { number: 1, document: body.document } }, 201)
      }
      if (path === '/api/v1/dashboards/dash-plugin/personal' && init.method === 'PUT') {
        const body = JSON.parse(String(init.body))
        return response({ id: 'dash-plugin', name: 'Plugins', scope: 'personal', draft: { number: 2, document: body.document } })
      }
      throw new Error(`unexpected request ${path}`)
    }))

    render(<DashboardManagement role="viewer" devices={devices}/>)
    await screen.findByText('暂无仪表盘')
    fireEvent.change(screen.getByLabelText('仪表盘名称'), { target: { value: 'Plugins' } })
    fireEvent.click(screen.getByRole('button', { name: '新建个人仪表盘' }))
    fireEvent.change(await screen.findByLabelText('指标来源'), { target: { value: 'plugin' } })
    await waitFor(() => expect((screen.getByLabelText('插件指标') as HTMLSelectElement).value).toContain('smart'))
    fireEvent.click(screen.getByRole('button', { name: '添加时间序列' }))
    expect(screen.getByText('历史请在 Explore 查询')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '读取当前值' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '保存布局' }))

    await waitFor(() => expect(calls.some(call => call.path.endsWith('/personal') && call.init.method === 'PUT')).toBe(true))
    const saved = JSON.parse(String(calls.find(call => call.path.endsWith('/personal'))!.init.body))
    expect(saved.document.widgets).toEqual([expect.objectContaining({
      id: 'plugin-widget-1', device_id: 'node-a',
      plugin_metric: { plugin_id: 'smart', plugin_version: '1.0.0', collector_id: 'health', metric_id: 'temperature' },
    })])
    expect(saved.document.widgets[0]).not.toHaveProperty('descriptor_id')
    expect(calls.some(call => call.path.includes('/plugin-metrics/history'))).toBe(false)
  })

  it('does not expose shared draft creation to Viewers', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/v1/dashboards') return response({ dashboards: [] })
      if (String(input) === '/api/v1/dashboard/metrics/descriptors') return response({ descriptors: [] })
      throw new Error('unexpected request')
    }))
    render(<DashboardManagement role="viewer"/>)
    expect(await screen.findByText('暂无仪表盘')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '新建共享草稿' })).not.toBeInTheDocument()
  })

  it('lets an Owner publish a shared draft with its expected revision', async () => {
    const calls: Array<{ path: string; init: RequestInit }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      calls.push({ path, init })
      if (path === '/api/v1/dashboards' && !init.method) return response({ dashboards: [] })
      if (path === '/api/v1/dashboard/metrics/descriptors') return response({ descriptors: [] })
      if (path === '/api/v1/dashboards' && init.method === 'POST') {
        const body = JSON.parse(String(init.body))
        return response({ id: 'shared-1', name: body.name, scope: 'shared', draft: { number: 4, document: body.document } }, 201)
      }
      if (path === '/api/v1/dashboards/shared-1/publish') {
        const body = JSON.parse(String(init.body))
        return response({ id: 'shared-1', name: 'Status', scope: 'shared', published: { number: body.expected_draft_revision, document: { widgets: [], layouts: [] } } })
      }
      throw new Error(`unexpected request ${path}`)
    }))
    render(<DashboardManagement role="owner"/>)
    await screen.findByText('暂无仪表盘')
    fireEvent.change(screen.getByLabelText('仪表盘名称'), { target: { value: 'Status' } })
    fireEvent.click(screen.getByRole('button', { name: '新建共享草稿' }))
    fireEvent.click(await screen.findByRole('button', { name: '发布草稿' }))
    await waitFor(() => expect(calls.some(call => call.path.endsWith('/publish'))).toBe(true))
    const publishCall = calls.find(call => call.path.endsWith('/publish'))!
    expect(JSON.parse(String(publishCall.init.body))).toEqual({ expected_draft_revision: 4 })
  })

  it('lets an Owner inspect published revisions and roll back as a new revision', async () => {
    const calls: Array<{ path: string; init: RequestInit }> = []
    const dashboard = {
      id: 'shared-history', name: 'History', scope: 'shared',
      draft: { number: 3, document: { widgets: [], layouts: [] } },
      published: { number: 2, document: { widgets: [{ id: 'current', kind: 'metric_card', title: 'Current' }], layouts: [] } },
    }
    let rolledBack = false
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      calls.push({ path, init })
      if (path === '/api/v1/dashboards' && !init.method) return response({ dashboards: [dashboard] })
      if (path === '/api/v1/dashboard/metrics/descriptors') return response({ descriptors: [] })
      if (path === '/api/v1/dashboards/shared-history') return response(rolledBack ? { ...dashboard, published: { ...dashboard.published, number: 3 } } : dashboard)
      if (path === '/api/v1/dashboards/shared-history/revisions') return response({ revisions: rolledBack
        ? [3, 2, 1].map(number => ({ number, document: { widgets: number === 1 ? [{ id: 'old', kind: 'time_series', title: 'Old', descriptor_id: 'cpu.old' }] : [{ id: 'current', kind: 'metric_card', title: 'Current' }], layouts: [] } }))
        : [2, 1].map(number => ({ number, document: { widgets: number === 2 ? [{ id: 'current', kind: 'metric_card', title: 'Current' }] : [{ id: 'old', kind: 'time_series', title: 'Old', descriptor_id: 'cpu.old' }], layouts: [] } })) })
      if (path === '/api/v1/dashboards/shared-history/rollback') {
        rolledBack = true
        return response({ ...dashboard, published: { ...dashboard.published, number: 3, document: { widgets: [{ id: 'old', kind: 'metric_card', title: 'Old' }], layouts: [] } } })
      }
      throw new Error(`unexpected request ${path}`)
    }))

    render(<DashboardManagement role="owner"/>)
    fireEvent.click(await screen.findByRole('button', { name: /History/ }))
    fireEvent.click(await screen.findByRole('button', { name: '查看发布历史' }))
    expect(await screen.findByText('版本 2 · 1 个 Widget · 当前发布')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '查看版本 1 内容' }))
    expect(screen.getByRole('region', { name: '版本 1 内容预览' })).toHaveTextContent('Old · time_series · cpu.old')
    fireEvent.click(screen.getByRole('button', { name: '回滚到版本 1' }))

    await screen.findByText('版本 3 · 1 个 Widget · 当前发布')
    expect(screen.getByText('版本 1 · 1 个 Widget')).toBeInTheDocument()
    const rollbackCall = calls.find(call => call.path.endsWith('/rollback'))
    expect(rollbackCall?.init.method).toBe('POST')
    expect(JSON.parse(String(rollbackCall?.init.body))).toEqual({ target_published_revision: 1, expected_published_revision: 2 })
  })

  it('allows a Viewer to browse and copy a published shared Dashboard without editing it', async () => {
    const dashboard = {
      id: 'shared-1', name: 'Shared status', scope: 'shared',
      published: { number: 3, document: { widgets: [{ id: 'cpu', kind: 'time_series', title: 'CPU', descriptor_id: 'cpu.usage_percent' }], layouts: [] } },
    }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/v1/dashboards') return response({ dashboards: [dashboard] })
      if (path === '/api/v1/dashboard/metrics/descriptors') return response({ descriptors: [] })
      if (path === '/api/v1/dashboards/shared-1') return response(dashboard)
      throw new Error(`unexpected request ${path}`)
    }))
    render(<DashboardManagement role="viewer"/>)
    fireEvent.click(await screen.findByRole('button', { name: /Shared status/ }))
    expect(await screen.findByText('CPU')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '保存布局' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '添加时间序列' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '移除' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '复制为个人版' })).toBeInTheDocument()
  })
})

function response(data: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => data }
}
