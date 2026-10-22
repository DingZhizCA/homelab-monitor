/**
 * App authentication-gate tests protect monitoring traffic from unauthenticated views.
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '@/App'

class TestEventSource {
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  addEventListener() {}
  close() {}
  readonly url: string

  constructor(url: string) { this.url = url }
}

afterEach(() => {
  cleanup()
  localStorage.clear()
  vi.unstubAllGlobals()
})

describe('App session gate', () => {
  it('does not query monitoring data or signal presence until login succeeds', async () => {
    let authenticated = false
    const eventSources: TestEventSource[] = []
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input)
      if (path === '/api/v1/auth/session') return { ok: authenticated, status: authenticated ? 200 : 401, json: async () => ({}) }
      if (path === '/api/v1/auth/login') { authenticated = true; return { ok: true, status: 200, json: async () => ({}) } }
      if (path === '/api/v1/auth/logout') { authenticated = false; return { ok: true, status: 204, init } }
      if (path === '/api/v1/devices') return { ok: true, status: 200, json: async () => [] }
      if (path === '/api/v1/presence') return { ok: true, status: 200, json: async () => ({}) }
      throw new Error(`unexpected request ${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', class extends TestEventSource {
      constructor(url: string) { super(url); eventSources.push(this) }
    })
    vi.stubGlobal('matchMedia', () => ({ matches: false }))

    render(<App/>)
    expect(await screen.findByRole('heading', { name: '登录 Home Lab Monitor' })).toBeInTheDocument()
    expect(fetchMock.mock.calls.map(([input]) => String(input))).not.toContain('/api/v1/devices')
    expect(fetchMock.mock.calls.map(([input]) => String(input))).not.toContain('/api/v1/presence')
    expect(eventSources).toHaveLength(0)

    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'owner' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))

    await waitFor(() => expect(screen.getByText('还没有设备')).toBeInTheDocument())
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toContain('/api/v1/devices')
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toContain('/api/v1/presence')
    expect(eventSources.map(source => source.url)).toContain('/api/v1/events')

    fireEvent.click(screen.getByRole('button', { name: '注销' }))
    expect(await screen.findByRole('heading', { name: '登录 Home Lab Monitor' })).toBeInTheDocument()
    const logoutCall = fetchMock.mock.calls.find(([input]) => String(input) === '/api/v1/auth/logout')
    expect(logoutCall?.[1]).toMatchObject({ method: 'POST', credentials: 'same-origin' })
  })

  it('loads the session role and opens account management for an Owner', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/v1/auth/session') return { ok: true, status: 200, json: async () => ({ user: { role: 'owner' } }) }
      if (path === '/api/v1/auth/users') return { ok: true, status: 200, json: async () => ({ users: [] }) }
      if (path === '/api/v1/devices') return { ok: true, status: 200, json: async () => [] }
      if (path === '/api/v1/presence') return { ok: true, status: 200, json: async () => ({}) }
      throw new Error(`unexpected request ${path}`)
    }))
    vi.stubGlobal('EventSource', TestEventSource)
    vi.stubGlobal('matchMedia', () => ({ matches: false }))
    render(<App/>)

    fireEvent.click(await screen.findByRole('button', { name: '账户' }))
    expect(screen.getByText('创建本地账户')).toBeInTheDocument()
  })

  it('opens personal Dashboard management only after an authenticated session', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/v1/auth/session') return { ok: true, status: 200, json: async () => ({ user: { role: 'viewer' } }) }
      if (path === '/api/v1/devices') return { ok: true, status: 200, json: async () => [] }
      if (path === '/api/v1/presence') return { ok: true, status: 200, json: async () => ({}) }
      if (path === '/api/v1/dashboards') return { ok: true, status: 200, json: async () => ({ dashboards: [] }) }
      if (path === '/api/v1/dashboard/metrics/descriptors') return { ok: true, status: 200, json: async () => ({ descriptors: [] }) }
      throw new Error(`unexpected request ${path}`)
    }))
    vi.stubGlobal('EventSource', TestEventSource)
    vi.stubGlobal('matchMedia', () => ({ matches: false }))
    render(<App/>)

    fireEvent.click(await screen.findByRole('button', { name: '仪表盘' }))
    expect(await screen.findByRole('heading', { name: '仪表盘' })).toBeInTheDocument()
    expect(await screen.findByText('暂无仪表盘')).toBeInTheDocument()
  })
})
