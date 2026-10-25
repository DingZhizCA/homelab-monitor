/**
 * Home Lab Monitor 的一级页面导航。
 * 只展示当前身份可访问且已有数据边界支持的一级视图。
 */
import { Activity, BellRing, ChartNoAxesCombined, LayoutDashboard, Network, Settings } from 'lucide-react'

export type AppPage = 'dashboard' | 'dashboards' | 'network' | 'explore' | 'alerts' | 'settings'

const pages: { id: AppPage; label: string; icon: typeof Activity; ownerOnly?: boolean; authOnly?: boolean }[] = [
  { id: 'dashboard', label: '概览', icon: Activity },
  { id: 'dashboards', label: '仪表盘', icon: LayoutDashboard, authOnly: true },
  { id: 'network', label: '网络接口', icon: Network },
  { id: 'explore', label: '临时对比', icon: ChartNoAxesCombined },
  { id: 'alerts', label: '告警', icon: BellRing, authOnly: true },
  { id: 'settings', label: '账户', icon: Settings, ownerOnly: true },
]

export function AppNavigation({ current, onSelect, showOwnerPages = false, showAuthenticatedPages = false }: { current: AppPage; onSelect: (page: AppPage) => void; showOwnerPages?: boolean; showAuthenticatedPages?: boolean }) {
  return <nav aria-label="主导航" className="flex gap-2 overflow-x-auto border-b px-4 py-2 sm:px-6">
    {pages.filter(page => (!page.ownerOnly || showOwnerPages) && (!page.authOnly || showAuthenticatedPages)).map(({ id, label, icon: Icon }) => <button
      key={id}
      type="button"
      aria-current={current === id ? 'page' : undefined}
      onClick={() => onSelect(id)}
      className={`inline-flex shrink-0 items-center gap-2 rounded-lg px-3 py-2 text-sm transition ${current === id ? 'bg-emerald-500/10 font-medium text-emerald-700 dark:text-emerald-300' : 'text-muted-foreground hover:bg-muted hover:text-foreground'}`}
    ><Icon size={16}/>{label}</button>)}
  </nav>
}
