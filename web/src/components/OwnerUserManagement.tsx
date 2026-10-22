/**
 * Owner-only account creation using the Server's protected local-user API.
 * This view never retains or displays a submitted password after success.
 */
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { UserPlus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

type AccountSummary = { id: string; username: string; role: 'owner' | 'viewer'; disabled: boolean }

export function OwnerUserManagement() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<'owner' | 'viewer'>('viewer')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [users, setUsers] = useState<AccountSummary[]>([])
  const [usersLoading, setUsersLoading] = useState(true)
  const [usersError, setUsersError] = useState('')
  const [updatingID, setUpdatingID] = useState('')

  const loadUsers = useCallback(async (signal?: AbortSignal) => {
    try {
      const response = await fetch('/api/v1/auth/users', { credentials: 'same-origin', signal })
      if (!response.ok) throw new Error(response.status === 401 || response.status === 403 ? '需要有效的 Owner 会话才能查看账户。' : '账户列表暂不可用。')
      const result = await response.json() as { users: AccountSummary[] }
      setUsers(result.users)
    } catch (loadError) {
      if (loadError instanceof Error && loadError.name === 'AbortError') return
      setUsersError(loadError instanceof Error ? loadError.message : '账户列表暂不可用。')
    } finally {
      if (!signal?.aborted) setUsersLoading(false)
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/auth/users', { credentials: 'same-origin', signal: controller.signal })
      .then(async response => {
        if (!response.ok) throw new Error(response.status === 401 || response.status === 403 ? '需要有效的 Owner 会话才能查看账户。' : '账户列表暂不可用。')
        return await response.json() as { users: AccountSummary[] }
      })
      .then(result => setUsers(result.users))
      .catch(loadError => {
        if (loadError instanceof Error && loadError.name === 'AbortError') return
        setUsersError(loadError instanceof Error ? loadError.message : '账户列表暂不可用。')
      })
      .finally(() => { if (!controller.signal.aborted) setUsersLoading(false) })
    return () => controller.abort()
  }, [])

  async function createUser(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setMessage('')
    setError('')
    setSubmitting(true)
    try {
      const response = await fetch('/api/v1/auth/users', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password, role }),
      })
      if (response.status === 201) {
        setUsername('')
        setPassword('')
        setMessage(`已创建 ${role === 'owner' ? 'Owner' : 'Viewer'} 账户。`)
        setUsersLoading(true)
        setUsersError('')
        await loadUsers()
        return
      }
      if (response.status === 401 || response.status === 403) {
        setError('当前会话无权创建账户，请重新登录 Owner 账户。')
      } else if (response.status === 409) {
        setError('账户名已被使用。')
      } else if (response.status === 400) {
        setError('账户信息无效，请检查输入。')
      } else {
        setError('账户创建失败，请稍后重试。')
      }
    } catch {
      setError('无法连接账户服务。')
    } finally {
      setSubmitting(false)
    }
  }

  async function updateUser(user: AccountSummary, changes: Partial<Pick<AccountSummary, 'role' | 'disabled'>>) {
    setError('')
    setMessage('')
    setUpdatingID(user.id)
    try {
      const response = await fetch('/api/v1/auth/users', {
        method: 'PUT',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ user_id: user.id, role: changes.role ?? user.role, disabled: changes.disabled ?? user.disabled }),
      })
      if (response.status === 204) {
        setMessage(`已更新账户 ${user.username}。`)
        await loadUsers()
      } else if (response.status === 409) {
        setError('至少需要保留一个启用的 Owner 账户。')
      } else if (response.status === 401 || response.status === 403) {
        setError('当前会话无权修改账户，请重新登录 Owner 账户。')
      } else if (response.status === 404) {
        setError('账户不存在，正在刷新账户列表。')
        await loadUsers()
      } else {
        setError('账户更新失败，请稍后重试。')
      }
    } catch {
      setError('无法连接账户服务。')
    } finally {
      setUpdatingID('')
    }
  }

  return <section className="mx-auto w-full max-w-xl">
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-lg"><UserPlus size={18}/>创建本地账户</CardTitle>
        <CardDescription>此操作由 Server 再次校验当前 Owner 会话。</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-4" onSubmit={createUser}>
          <label className="grid gap-1.5 text-sm font-medium">用户名
            <input autoComplete="username" required value={username} onChange={event => setUsername(event.target.value)} className="h-10 rounded-md border bg-background px-3 font-normal outline-none focus-visible:ring-2 focus-visible:ring-ring"/>
          </label>
          <label className="grid gap-1.5 text-sm font-medium">初始密码
            <input type="password" autoComplete="new-password" maxLength={1024} required value={password} onChange={event => setPassword(event.target.value)} className="h-10 rounded-md border bg-background px-3 font-normal outline-none focus-visible:ring-2 focus-visible:ring-ring"/>
          </label>
          <label className="grid gap-1.5 text-sm font-medium">角色
            <select value={role} onChange={event => setRole(event.target.value as 'owner' | 'viewer')} className="h-10 rounded-md border bg-background px-3 font-normal outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <option value="viewer">Viewer</option>
              <option value="owner">Owner</option>
            </select>
          </label>
          {message && <p role="status" className="text-sm text-emerald-700 dark:text-emerald-300">{message}</p>}
          {error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">{error}</p>}
          <Button type="submit" disabled={submitting}><UserPlus/>{submitting ? '正在创建…' : '创建账户'}</Button>
        </form>
        <div className="mt-6 border-t pt-4">
          <h3 className="text-sm font-semibold">现有账户</h3>
          {usersLoading ? <p className="mt-2 text-sm text-muted-foreground" role="status">正在加载账户…</p> : usersError ? <p className="mt-2 text-sm text-destructive" role="alert">{usersError}</p> : users.length === 0 ? <p className="mt-2 text-sm text-muted-foreground">暂无账户。</p> : <ul className="mt-2 divide-y rounded-lg border">{users.map(user => <li key={user.id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm"><span className="min-w-24 flex-1 truncate">{user.username}</span><select aria-label={`角色-${user.username}`} disabled={updatingID !== ''} value={user.role} onChange={event => void updateUser(user, { role: event.target.value as AccountSummary['role'] })} className="h-8 rounded-md border bg-background px-2 text-xs"><option value="owner">Owner</option><option value="viewer">Viewer</option></select><Button type="button" size="sm" variant="outline" disabled={updatingID !== ''} onClick={() => void updateUser(user, { disabled: !user.disabled })}>{updatingID === user.id ? '更新中…' : user.disabled ? '启用' : '停用'}</Button></li>)}</ul>}
        </div>
      </CardContent>
    </Card>
  </section>
}
