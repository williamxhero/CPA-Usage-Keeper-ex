import { CHART_ANIMATION_MS, useChartAnimation, type ChartRefreshSignal } from '../charts/chartLifecycle';

export const MODEL_SPEED_ANIMATION_MS = CHART_ANIMATION_MS;
export type ModelSpeedRefreshSignal = ChartRefreshSignal;

export function useModelSpeedAnimation(queryKey: string, source: object | undefined, refreshSignal?: ModelSpeedRefreshSignal) {
  return useChartAnimation<'scatter'>({ queryKey, source, refreshSignal });
}
