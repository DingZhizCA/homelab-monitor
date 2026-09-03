/**
 * ECharts 生命周期适配器。
 * 负责按主题创建实例、响应容器尺寸变化并在卸载时释放资源，图表业务定义由调用方提供。
 */
import { useEffect, useRef } from 'react'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsOption } from 'echarts'

echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

/** Render one responsive ECharts option with an accessible text label. */
export function MetricChart({ option, label }: { option: EChartsOption; label: string }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!ref.current) return
    const chart = echarts.init(ref.current)
    chart.setOption(option)
    const observer = new ResizeObserver(() => chart.resize())
    observer.observe(ref.current)
    return () => { observer.disconnect(); chart.dispose() }
  }, [option])
  return <div ref={ref} className="h-56 w-full sm:h-64" role="img" aria-label={label} />
}
