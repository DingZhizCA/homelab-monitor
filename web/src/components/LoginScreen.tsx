/**
 * Local account login form shown when the Server enables human sessions.
 * It sends credentials only to the same-origin HTTPS authentication endpoint.
 */
import { useState, type FormEvent } from 'react'
import { Activity, LockKeyhole } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

export function LoginScreen({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [mode, setMode] = useState<'login' | 'setup'>('login')
  const [submitting, setSubmitting] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setSubmitting(true)
    try {
      const response = await fetch(mode === 'setup' ? '/api/v1/auth/bootstrap' : '/api/v1/auth/login', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })
      if (mode === 'setup' && response.status === 201) {
        setPassword('')
        setError('')
        setNotice('Owner 账户已创建，请使用新账户登录。')
        setMode('login')
        return
      }
      if (mode === 'setup' && response.status === 409) {
        setPassword('')
        setError('首次设置未完成；请确认账户信息，或使用已有账户登录。')
        setMode('login')
        return
      }
      if (response.status === 401) {
        setError('用户名或密码不正确。')
        return
      }
      if (!response.ok) {
        setError(response.status === 403 ? '认证操作需要通过已配置的 HTTPS 源站访问。' : mode === 'setup' ? '无法创建 Owner 账户，请检查输入后重试。' : '当前无法登录，请稍后重试。')
        return
      }
      setPassword('')
      onAuthenticated()
    } catch {
      setError('无法连接到监控服务。')
    } finally {
      setSubmitting(false)
    }
  }

  return <main className="grid min-h-svh place-items-center bg-background px-4 py-8 text-foreground">
    <Card className="w-full max-w-sm">
      <CardHeader className="text-center">
        <span className="mx-auto mb-2 grid size-11 place-items-center rounded-xl bg-emerald-500 text-white"><Activity size={22}/></span>
        <CardTitle><h1>{mode === 'setup' ? '首次设置 Owner' : '登录 Home Lab Monitor'}</h1></CardTitle>
        <CardDescription>{mode === 'setup' ? '创建本机的第一个管理员账户。' : '使用本地监控账户继续。'}</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-4" onSubmit={submit}>
          <label className="grid gap-1.5 text-sm font-medium">用户名
            <input autoComplete="username" required value={username} onChange={event => setUsername(event.target.value)} className="h-10 rounded-md border bg-background px-3 font-normal outline-none focus-visible:ring-2 focus-visible:ring-ring"/>
          </label>
          <label className="grid gap-1.5 text-sm font-medium">密码
            <input type="password" autoComplete={mode === 'setup' ? 'new-password' : 'current-password'} maxLength={1024} required value={password} onChange={event => setPassword(event.target.value)} className="h-10 rounded-md border bg-background px-3 font-normal outline-none focus-visible:ring-2 focus-visible:ring-ring"/>
          </label>
          {notice && <p role="status" className="text-sm text-emerald-700 dark:text-emerald-300">{notice}</p>}
          {error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">{error}</p>}
          <Button type="submit" disabled={submitting} className="w-full"><LockKeyhole/>{submitting ? '正在处理…' : mode === 'setup' ? '创建 Owner' : '登录'}</Button>
          <Button type="button" variant="ghost" disabled={submitting} onClick={() => { setError(''); setNotice(''); setMode(mode === 'login' ? 'setup' : 'login') }}>{mode === 'setup' ? '返回登录' : '首次设置 Owner'}</Button>
        </form>
      </CardContent>
    </Card>
  </main>
}
