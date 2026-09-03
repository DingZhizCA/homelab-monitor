import { describe, expect, it } from 'vitest'
import { appendPoint, MAX_HISTORY_POINTS } from './history'
import type { Point } from '@/types'

describe('appendPoint', () => {
  it('keeps only the latest 60 samples', () => {
    let points: Point[] = Array.from({ length: MAX_HISTORY_POINTS }, (_, time) => ({ time, cpu: time, memory: time, rx: time, tx: time, temperature: 40, temperature_time: time }))
    points = appendPoint(points, { time: 60, cpu: 60, memory: 60, rx: 60, tx: 60, temperature: 41, temperature_time: 60 })
    expect(points).toHaveLength(60)
    expect(points[0].time).toBe(1)
    expect(points.at(-1)?.time).toBe(60)
  })
})
