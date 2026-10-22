/**
 * Login form tests for same-origin session exchange and safe error display.
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LoginScreen } from '@/components/LoginScreen'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('LoginScreen', () => {
  it('sends credentials to the same-origin session endpoint and notifies on success', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    const onAuthenticated = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    render(<LoginScreen onAuthenticated={onAuthenticated}/>)

    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'owner' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))

    await waitFor(() => expect(onAuthenticated).toHaveBeenCalledOnce())
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/login', expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'owner', password: 'secret' }),
    }))
  })

  it('shows a generic credential error without logging the password', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 401 })
    vi.stubGlobal('fetch', fetchMock)
    render(<LoginScreen onAuthenticated={vi.fn()}/>)
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'owner' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('用户名或密码不正确。')
    expect(screen.queryByText('secret')).not.toBeInTheDocument()
  })

  it('creates the first Owner through the same-origin bootstrap endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 201 })
    vi.stubGlobal('fetch', fetchMock)
    render(<LoginScreen onAuthenticated={vi.fn()}/>)
    fireEvent.click(screen.getByRole('button', { name: '首次设置 Owner' }))
    expect(screen.getByRole('heading', { name: '首次设置 Owner' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'owner' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'long-enough-secret' } })
    fireEvent.click(screen.getByRole('button', { name: '创建 Owner' }))

    expect(await screen.findByRole('status')).toHaveTextContent('Owner 账户已创建，请使用新账户登录。')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/bootstrap', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      body: JSON.stringify({ username: 'owner', password: 'long-enough-secret' }),
    }))
  })

  it('returns to login when first Owner setup has already been claimed', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 409 })
    vi.stubGlobal('fetch', fetchMock)
    render(<LoginScreen onAuthenticated={vi.fn()}/>)
    fireEvent.click(screen.getByRole('button', { name: '首次设置 Owner' }))
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'owner' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: '创建 Owner' }))

    expect(await screen.findByRole('heading', { name: '登录 Home Lab Monitor' })).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('首次设置未完成')
  })
})
