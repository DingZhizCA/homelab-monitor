/**
 * Dashboard 的浏览器内临时历史窗口。
 * 数据仅用于当前页面趋势展示，不代表 Server 已持久化历史指标。
 */
import type { Point } from '@/types'

export const MAX_HISTORY_POINTS = 60

/** Append one sample while retaining only the newest bounded window. */
export function appendPoint(points: Point[], point: Point): Point[] {
  return [...points, point].slice(-MAX_HISTORY_POINTS)
}
