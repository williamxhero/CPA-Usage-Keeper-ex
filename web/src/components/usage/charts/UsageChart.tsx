import { useLayoutEffect, useMemo, useRef } from 'react';
import { Chart as NativeChart, Bar as NativeBar, Line as NativeLine, Doughnut as NativeDoughnut, Scatter as NativeScatter } from 'react-chartjs-2';
import type { ChartOptions, ChartType, Plugin } from 'chart.js';
import { CHART_ANIMATION_MS, useChartAnimation } from './chartLifecycle';

// 仅统一图表生命周期，保留原库组件的 ref、数据类型、插件和事件 API。
function useLifecycleProps<T extends ChartType>(options: ChartOptions<T> | undefined, plugins: Plugin<T>[] | undefined) {
  const { reducedMotion, beforeDatasetsUpdate } = useChartAnimation<T>();
  const live = useRef(beforeDatasetsUpdate);
  useLayoutEffect(() => { live.current = beforeDatasetsUpdate; }, [beforeDatasetsUpdate]);
  const plugin = useMemo<Plugin<T>>(() => ({
    id: 'keeperChartLifecycle',
    beforeDatasetsUpdate: chart => live.current(chart),
  }), []);
  const animatedOptions = useMemo<ChartOptions<T>>(() => ({
    ...options,
    animation: reducedMotion ? false : { ...(typeof options?.animation === 'object' ? options.animation : {}), duration: CHART_ANIMATION_MS },
    transitions: { ...options?.transitions, resize: { animation: { duration: reducedMotion ? 0 : CHART_ANIMATION_MS } } },
  } as ChartOptions<T>), [options, reducedMotion]);
  const chartPlugins = useMemo(() => [...(plugins ?? []), plugin], [plugin, plugins]);
  return { options: animatedOptions, plugins: chartPlugins };
}

export const Chart: typeof NativeChart = props => <NativeChart {...props} {...useLifecycleProps(props.options, props.plugins)} />;
export const Bar: typeof NativeBar = props => <NativeBar {...props} {...useLifecycleProps(props.options, props.plugins)} />;
export const Line: typeof NativeLine = props => <NativeLine {...props} {...useLifecycleProps(props.options, props.plugins)} />;
export const Doughnut: typeof NativeDoughnut = props => <NativeDoughnut {...props} {...useLifecycleProps(props.options, props.plugins)} />;
export const Scatter: typeof NativeScatter = props => <NativeScatter {...props} {...useLifecycleProps(props.options, props.plugins)} />;
