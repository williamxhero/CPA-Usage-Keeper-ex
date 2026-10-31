import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';
import type { Chart, ChartType } from 'chart.js';
import { useMediaQuery } from '@/hooks/useMediaQuery';

export const CHART_ANIMATION_MS = 500;
export type ChartRefreshSignal = { source: object; revision: number };
type ChartScope = { queryKey: string; source?: object | null; refreshSignal?: ChartRefreshSignal };
const ChartLifecycleContext = createContext<ChartScope>({ queryKey: '' });

// 数据源各自绑定已提交的成功响应，手动刷新失败或被新请求取代不能触发入场。
export function useChartRefresh() {
  const [signal, setSignal] = useState<ChartRefreshSignal>();
  const markLoaded = useCallback((source: object) => {
    setSignal(previous => ({ source, revision: (previous?.revision ?? 0) + 1 }));
  }, []);
  return [signal, markLoaded] as const;
}

export function ChartLifecycleProvider({ children, ...scope }: ChartScope & { children: ReactNode }) {
  return <ChartLifecycleContext.Provider value={scope}>{children}</ChartLifecycleContext.Provider>;
}

export function useChartAnimation<T extends ChartType>(scope?: ChartScope) {
  const inherited = useContext(ChartLifecycleContext);
  const { queryKey, source, refreshSignal } = scope ?? inherited;
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)');
  const previous = useRef<{ chart: Chart<T>; queryKey: string; source?: object | null; revision: number } | null>(null);
  const beforeDatasetsUpdate = useCallback((chart: Chart<T>) => {
    const last = previous.current;
    // 查询已切换但仍是上一份响应时，resize/theme 更新不能提前消费新查询。
    if (last?.chart === chart && last.queryKey !== queryKey && source != null && source === last.source) return;
    const revision = refreshSignal && refreshSignal.source === source ? refreshSignal.revision : null;
    const replay = !last || last.chart !== chart || last.queryKey !== queryKey || (revision !== null && revision !== last.revision);
    previous.current = { chart, queryKey, source, revision: revision ?? last?.revision ?? 0 };
    // 在布局后复用原生 reset/update；自动刷新、选择和尺寸变化从当前坐标继续。
    if (replay && !reducedMotion) { chart.stop(); chart.reset(); }
  }, [queryKey, source, refreshSignal, reducedMotion]);
  return { reducedMotion, beforeDatasetsUpdate };
}
