/** Owner account creation tests cover the HTTPS same-origin management boundary. */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { OwnerUserManagement } from '@/components/OwnerUserManagement'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('OwnerUserManagement', () => {
  it('creates a Viewer account and clears the initial password', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => String(input) === '/api/v1/auth/users' && init.method !== 'POST' && init.method !== 'PUT'
      ? { ok: true, status: 200, json: async () => ({ users: [{ id: 'u1', username: 'viewer-a', role: 'viewer', disabled: false }] }) }
      : init.method === 'PUT' ? { ok: true, status: 204 } : { ok: true, status: 201 })
    vi.stubGlobal('fetch', fetchMock)
    render(<OwnerUserManagement/>)
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'viewer-a' } })
    fireEvent.change(screen.getByLabelText('初始密码'), { target: { value: 'initial-secret' } })
    fireEvent.click(screen.getByRole('button', { name: '创建账户' }))

    expect(await screen.findByText('已创建 Viewer 账户。')).toBeInTheDocument()
    expect(await screen.findByText('viewer-a')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText('初始密码')).toHaveValue(''))
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/users', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      body: JSON.stringify({ username: 'viewer-a', password: 'initial-secret', role: 'viewer' }),
    }))
    fireEvent.change(screen.getByLabelText('角色-viewer-a'), { target: { value: 'owner' } })
    expect(await screen.findByText('已更新账户 viewer-a。')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/users', expect.objectContaining({
      method: 'PUT',
      credentials: 'same-origin',
      body: JSON.stringify({ user_id: 'u1', role: 'owner', disabled: false }),
    }))
  })

  it('reports a duplicate account without exposing submitted credentials', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => String(input) === '/api/v1/auth/users' && init.method !== 'POST' && init.method !== 'PUT'
      ? { ok: true, status: 200, json: async () => ({ users: [] }) }
      : { ok: false, status: 409 })
    vi.stubGlobal('fetch', fetchMock)
    render(<OwnerUserManagement/>)
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'viewer-a' } })
    fireEvent.change(screen.getByLabelText('初始密码'), { target: { value: 'initial-secret' } })
    fireEvent.click(screen.getByRole('button', { name: '创建账户' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('账户名已被使用。')
    expect(screen.queryByText('initial-secret')).not.toBeInTheDocument()
  })
})
