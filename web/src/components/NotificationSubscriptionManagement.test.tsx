/**
 * Notification subscription UI tests preserve explicit loading and Session-bound writes.
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NotificationSubscriptionManagement } from '@/components/NotificationSubscriptionManagement'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('NotificationSubscriptionManagement', () => {
  it('does not read until the user provides a limit and requests it', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL) => ({ ok: true, status: 200, json: async () => ({ subscriptions: [] }) }))
    vi.stubGlobal('fetch', fetchMock)
    render(<NotificationSubscriptionManagement deviceID="node-a"/>)
    expect(fetchMock).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('订阅条数上限'), { target: { value: '4' } })
    fireEvent.click(screen.getByRole('button', { name: '读取我的订阅' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(String(fetchMock.mock.calls[0][0])).toContain('limit=4')
	  expect(String(fetchMock.mock.calls[0][0])).toContain('device_id=node-a')
    expect(await screen.findByText('该设备还没有我的订阅。')).toBeInTheDocument()
  })

  it('creates an exact subscription without sending a user identity', async () => {
    let body: Record<string, unknown> = {}
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
      body = JSON.parse(String(init.body)) as Record<string, unknown>
      return { ok: true, status: 204, json: async () => ({}) }
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<NotificationSubscriptionManagement deviceID="node-a"/>)
    fireEvent.click(screen.getByRole('button', { name: '新建订阅' }))
    fireEvent.change(screen.getByLabelText('订阅规则 ID'), { target: { value: 'high-cpu' } })
    fireEvent.change(screen.getByLabelText('订阅严重级别'), { target: { value: 'warning' } })
    fireEvent.change(screen.getByLabelText('订阅启用状态'), { target: { value: 'true' } })
    fireEvent.click(screen.getByLabelText('站内通知渠道'))
    fireEvent.click(screen.getByRole('button', { name: '保存新 Revision' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(body).toEqual({
      expected_revision: 0,
      subscription: { device_id: 'node-a', rule_id: 'high-cpu', severity: 'warning', revision: 1, enabled: true, channels: { in_app: true, push: false } },
    })
    expect(await screen.findByRole('status')).toHaveTextContent('Revision 1 已保存')
  })
})
