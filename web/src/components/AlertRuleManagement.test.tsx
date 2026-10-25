/**
 * Rule-management tests protect explicit business inputs and versioned writes.
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AlertRuleManagement } from '@/components/AlertRuleManagement'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('AlertRuleManagement', () => {
  it('requires explicit inputs and creates Revision 1 with expected current zero', async () => {
    let savedBody: unknown
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      if (path.startsWith('/api/v1/owner/alert-rules?')) return { ok: true, status: 200, json: async () => ({ rules: [] }) }
      if (path === '/api/v1/dashboard/metrics/descriptors') return { ok: true, status: 200, json: async () => ({ descriptors: [{ id: 'cpu.usage_percent', name: 'CPU', unit: 'percent', description: 'CPU usage' }] }) }
      if (path === '/api/v1/owner/alert-rules' && init.method === 'PUT') { savedBody = JSON.parse(String(init.body)); return { ok: true, status: 204 } }
      throw new Error(`unexpected request ${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AlertRuleManagement deviceID="node-a"/>)
    expect(await screen.findByText('该设备还没有规则。')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '新建规则' }))
    expect(screen.getByLabelText('严重级别')).toHaveValue('')
    expect(screen.getByLabelText('启用状态')).toHaveValue('')
    expect(screen.getByLabelText('规则运算符')).toHaveValue('')
    expect(screen.getByLabelText('规则阈值')).toHaveValue(null)
    fireEvent.change(screen.getByLabelText('规则 ID'), { target: { value: 'high-cpu' } })
    fireEvent.change(screen.getByLabelText('严重级别'), { target: { value: 'warning' } })
    fireEvent.change(screen.getByLabelText('启用状态'), { target: { value: 'true' } })
    fireEvent.change(screen.getByLabelText('规则指标'), { target: { value: 'cpu.usage_percent' } })
    fireEvent.change(screen.getByLabelText('规则运算符'), { target: { value: 'gt' } })
    fireEvent.change(screen.getByLabelText('规则阈值'), { target: { value: '80' } })
    fireEvent.click(screen.getByRole('button', { name: '保存新 Revision' }))

    expect(await screen.findByText('规则 high-cpu Revision 1 已保存。')).toBeInTheDocument()
    expect(savedBody).toEqual({ expected_current: 0, rule: {
      id: 'high-cpu', device_id: 'node-a', revision: 1, severity: 'warning', enabled: true,
      expression: { metric: { descriptor_id: 'cpu.usage_percent', labels: {}, operator: 'gt', threshold: 80, unit: 'percent' } },
    } })
  })

  it('keeps complex rules read-only and revisions a simple rule with CAS', async () => {
    let savedBody: { expected_current: number; rule: { revision: number; expression: { metric: { threshold: number } } } } | undefined
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      if (path.startsWith('/api/v1/owner/alert-rules?')) return { ok: true, status: 200, json: async () => ({ rules: [
        { id: 'high-cpu', device_id: 'node-a', revision: 3, severity: 'warning', enabled: true, expression: { metric: { descriptor_id: 'cpu.usage_percent', operator: 'gt', threshold: 80, unit: 'percent' } } },
        { id: 'combined', device_id: 'node-a', revision: 2, severity: 'critical', enabled: true, expression: { all: [{ metric: { descriptor_id: 'cpu.usage_percent', operator: 'gt', threshold: 90, unit: 'percent' } }] } },
      ] }) }
      if (path === '/api/v1/dashboard/metrics/descriptors') return { ok: true, status: 200, json: async () => ({ descriptors: [{ id: 'cpu.usage_percent', name: 'CPU', unit: 'percent', description: 'CPU usage' }] }) }
      if (path === '/api/v1/owner/alert-rules' && init.method === 'PUT') { savedBody = JSON.parse(String(init.body)); return { ok: true, status: 204 } }
      throw new Error(`unexpected request ${path}`)
    }))
    render(<AlertRuleManagement deviceID="node-a"/>)
    expect(await screen.findByText('复杂表达式只读')).toBeInTheDocument()
    const editButtons = screen.getAllByRole('button', { name: '修订' })
    expect(editButtons).toHaveLength(1)
    fireEvent.click(editButtons[0])
    expect(screen.getByLabelText('规则 ID')).toBeDisabled()
    fireEvent.change(screen.getByLabelText('规则阈值'), { target: { value: '85' } })
    fireEvent.click(screen.getByRole('button', { name: '保存新 Revision' }))
    await waitFor(() => expect(savedBody).toBeDefined())
    expect(savedBody?.expected_current).toBe(3)
    expect(savedBody?.rule.revision).toBe(4)
    expect(savedBody?.rule.expression.metric.threshold).toBe(85)
  })

  it('loads immutable history and republishes an explicit target as a new revision', async () => {
    let rollbackBody: unknown
    const current = { id: 'combined', device_id: 'node-a', revision: 3, severity: 'critical', enabled: true, expression: { all: [{ metric: { descriptor_id: 'cpu.usage_percent', operator: 'gt', threshold: 90, unit: 'percent' } }] } }
    const old = { ...current, revision: 1, severity: 'warning', expression: { all: [{ metric: { descriptor_id: 'cpu.usage_percent', operator: 'gt', threshold: 80, unit: 'percent' } }] } }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      if (path.startsWith('/api/v1/owner/alert-rules?')) return { ok: true, status: 200, json: async () => ({ rules: [current] }) }
      if (path === '/api/v1/dashboard/metrics/descriptors') return { ok: true, status: 200, json: async () => ({ descriptors: [] }) }
      if (path.startsWith('/api/v1/owner/alert-rules/revisions?')) return { ok: true, status: 200, json: async () => ({ revisions: [current, old] }) }
      if (path === '/api/v1/owner/alert-rules/revisions' && init.method === 'POST') { rollbackBody = JSON.parse(String(init.body)); return { ok: true, status: 204 } }
      throw new Error(`unexpected request ${path}`)
    }))
    render(<AlertRuleManagement deviceID="node-a"/>)
    expect(await screen.findByText('复杂表达式只读')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '历史' }))
    const rollback = await screen.findByRole('button', { name: '以 Revision 1 创建 Revision 4' })
    fireEvent.click(rollback)
    expect(await screen.findByText('规则 combined 已将 Revision 1 重新发布为 Revision 4。')).toBeInTheDocument()
    expect(rollbackBody).toEqual({ device_id: 'node-a', rule_id: 'combined', target_revision: 1, new_revision: 4, expected_current: 3 })
  })
})
