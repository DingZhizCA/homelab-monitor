/** In-app notification UI tests preserve explicit bounded reads. */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InAppNotificationList } from '@/components/InAppNotificationList'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('InAppNotificationList', () => {
  it('loads only after an explicit positive limit', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL) => ({ ok: true, status: 200, json: async () => ({ notifications: [] }) }))
    vi.stubGlobal('fetch', fetchMock)
    render(<InAppNotificationList/>)
    expect(fetchMock).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('通知条数上限'), { target: { value: '5' } })
    fireEvent.click(screen.getByRole('button', { name: '读取我的通知' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(String(fetchMock.mock.calls[0][0])).toContain('limit=5')
    expect(await screen.findByText('还没有站内通知。')).toBeInTheDocument()
  })
})
