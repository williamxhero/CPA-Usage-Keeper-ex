// @vitest-environment happy-dom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { Chart, ChartData, ChartOptions, Plugin } from 'chart.js';
import type { RealtimeModelSpeedPayload } from '@/lib/types';
import { RealtimeModelSpeedCard } from '../RealtimeModelSpeedCard';
import { modelSpeedStorageKey } from '../modelSpeedSelection';

let captured: { data: ChartData<'scatter', { x: number | null; y: number }[]>; options: ChartOptions<'scatter'>; plugins: Plugin<'scatter'>[] };
vi.mock('react-chartjs-2', () => ({ Scatter: (props: typeof captured) => { captured = props; return <canvas data-speed-chart />; } }));
vi.mock('@/i18n', () => ({ default: { language: 'en', t: (key: string) => key } }));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ i18n: { language: 'en' }, t: (key: string, values?: { samples: string }) => values ? `${key}: ${values.samples}` : key }) }));
globalThis.IS_REACT_ACT_ENVIRONMENT = true;
const speed: RealtimeModelSpeedPayload = { available: true, start: '2026-10-08T12:00:00+08:00', mid: '2026-10-08T12:07:30+08:00', end: '2026-10-08T12:15:00+08:00', series: [
  { model: 'both-halves', total_tokens: 1000, speed_sample_count: 4, first_half: { speed_tps_sum: 120, speed_sample_count: 3, mean_tps: 40 }, second_half: { speed_tps_sum: 80, speed_sample_count: 1, mean_tps: 80 } },
  { model: 'second-only', total_tokens: 100, speed_sample_count: 1, first_half: { speed_tps_sum: 0, speed_sample_count: 0, mean_tps: null }, second_half: { speed_tps_sum: 10, speed_sample_count: 1, mean_tps: 10 } },
  { model: 'no-speed', total_tokens: 3000, speed_sample_count: 0, first_half: { speed_tps_sum: 0, speed_sample_count: 0, mean_tps: null }, second_half: { speed_tps_sum: 0, speed_sample_count: 0, mean_tps: null } },
] };
let container: HTMLDivElement;
let root: Root;
beforeEach(() => { localStorage.clear(); container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
const render = async (response = speed, loading = false) => { await act(async () => root.render(<RealtimeModelSpeedCard speed={response} loading={loading} isDark={false} isMobile={false} timezone="Asia/Shanghai" />)); };
const hover = async (row: number, plugin = captured.plugins[0], type: 'mousemove' | 'click' | 'mouseout' = 'mousemove', x = 100) => {
  const chart = { getDatasetMeta: (index: number) => ({ data: [{ x: index % 2 === 0 ? 40 : 80, y: Math.floor(index / 2) }] }), canvas: container.querySelector('canvas'), chartArea: { left: 0, right: 300, top: 0, bottom: 300 }, scales: { y: { getValueForPixel: (value: number) => value } } } as unknown as Chart<'scatter'>;
  await act(async () => plugin.afterEvent!(chart, { event: { type, x, y: row, native: null }, replay: false, inChartArea: true }, {}));
};

it('uses two distinct markers per model, null single-side gaps, and one zero-based axis', async () => {
  await render();
  expect(captured.data.datasets).toHaveLength(6);
  expect(captured.data.datasets[2].data).toEqual([{ x: 40, y: 1 }]);
  expect(captured.data.datasets[3].data).toEqual([{ x: 80, y: 1 }]);
  expect(captured.data.datasets[2].backgroundColor).toBe('transparent');
  expect(captured.data.datasets[3].backgroundColor).toBe(captured.data.datasets[3].borderColor);
  expect(captured.data.datasets[4].data[0].x).toBeNull();
  expect(captured.data.datasets[5].data[0].x).toBe(10);
  expect(captured.options.scales?.x?.min).toBe(0);
});

it('shows weighted whole-window means in the legend without averaging the two half means', async () => {
  await render();
  const legend = container.querySelector('[data-model-speed-legend]')!;
  const rows = Array.from(legend.querySelectorAll('li'));
  expect(rows.find((row) => row.title === 'both-halves')!.textContent).toContain('50.0 t/s');
  expect(rows.find((row) => row.title === 'second-only')!.textContent).toContain('10.0 t/s');
  expect(rows.find((row) => row.title === 'no-speed')!.textContent).toContain('usage_stats.no_data');
});

it('shows complete model name and accurate two half time ranges with means and sample counts', async () => {
  await render();
  await hover(1);
  const tooltip = document.querySelector('[data-model-speed-tooltip]')!;
  expect(tooltip.textContent).toContain('both-halves');
  expect(tooltip.textContent).toContain('12:07:30');
  expect(tooltip.textContent).toContain('12:00:00');
  expect(tooltip.textContent).toContain('12:15:00');
  expect(tooltip.textContent).toContain('40.0 t/s');
  expect(tooltip.textContent).toContain('80.0 t/s');
  expect(tooltip.querySelectorAll('li')).toHaveLength(2);
  expect(tooltip.querySelector('li strong')!.textContent).toBe('usage_stats.model_speed_first_half');
  const value = tooltip.querySelector<HTMLElement>('[data-model-speed-value]')!;
  expect(value.textContent).toBe('40.0 t/s');
  expect(getComputedStyle(value).color).toBe('#111827');
  expect(tooltip.querySelector('li small')!.textContent).toBe('12:00:00 – 12:07:30 · usage_stats.model_speed_samples: 3');
  expect(tooltip.textContent).not.toContain('10/08/2026');
  await hover(0);
  expect(document.querySelector('[data-model-speed-tooltip]')!.textContent).toContain('usage_stats.no_data');
  expect(document.querySelector('[data-model-speed-tooltip]')!.textContent).not.toContain('0.0 t/s');
});

it('retains the registered plugin while response refresh updates values and selection stays fixed', async () => {
  await render();
  const initial = captured.plugins[0];
  const colors = captured.data.datasets.map((dataset) => dataset.borderColor);
  const refreshed = structuredClone(speed);
  refreshed.series[0].first_half.speed_tps_sum = 600;
  refreshed.series[1].total_tokens = 999999;
  await render(refreshed);
  expect(captured.plugins[0]).toBe(initial);
  expect(captured.data.datasets.map((dataset) => dataset.borderColor)).toEqual(colors);
  expect(captured.data.datasets[2].label).toBe('both-halves');
  await hover(1, initial);
  expect(document.querySelector('[data-model-speed-tooltip]')!.textContent).toContain('200.0 t/s');
});

it('lets the pointer pass through the tooltip, follows the next model and closes on canvas leave', async () => {
  await render();
  await hover(1);
  const tooltip = document.querySelector<HTMLElement>('[data-model-speed-tooltip]')!;
  expect(getComputedStyle(tooltip).pointerEvents).toBe('none');
  expect(tooltip.querySelector('button')).toBeNull();
  await hover(2);
  expect(document.querySelector('[data-model-speed-tooltip]')!.textContent).toContain('second-only');
  await hover(2, captured.plugins[0], 'mouseout');
  expect(document.querySelector('[data-model-speed-tooltip]')).toBeNull();
});

it('keeps a touch-selected model open on canvas leave and dismisses on an outside tap', async () => {
  await render();
  await act(async () => container.querySelector('canvas')!.dispatchEvent(new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true })));
  await hover(1, captured.plugins[0], 'click');
  await hover(1, captured.plugins[0], 'mouseout');
  expect(document.querySelector('[data-model-speed-tooltip]')!.textContent).toContain('both-halves');
  await act(async () => document.body.dispatchEvent(new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true })));
  expect(document.querySelector('[data-model-speed-tooltip]')).toBeNull();
});

it('shows cache-unavailable and explicit-empty selection states without zero speed data', async () => {
  await render({ available: false, series: [] });
  expect(container.textContent).toContain('usage_stats.model_speed_unavailable');
  expect(container.querySelector('canvas')).toBeNull();
  localStorage.setItem(modelSpeedStorageKey('realtime', 'admin'), JSON.stringify({ version: 1, models: [] }));
  await act(async () => root.unmount()); root = createRoot(container);
  await render();
  expect(container.textContent).toContain('usage_stats.model_speed_empty_selection');
  expect(container.querySelector('canvas')).toBeNull();
});

it('labels a double-null or missing selected model directly in its row without adding a zero point', async () => {
  localStorage.setItem(modelSpeedStorageKey('realtime', 'admin'), JSON.stringify({ version: 1, models: ['missing', 'second-only'] }));
  await render();
  expect(captured.data.datasets[0].data[0].x).toBeNull();
  expect(captured.data.datasets[1].data[0].x).toBeNull();
  const fillText = vi.fn();
  const chart = {
    ctx: { save: vi.fn(), restore: vi.fn(), fillText },
    scales: { x: { getPixelForValue: (value: number) => value }, y: { getPixelForValue: (value: number) => value * 50 } },
  } as unknown as Chart<'scatter'>;
  captured.plugins[0].beforeDatasetsDraw!(chart, { cancelable: true }, {});
  expect(fillText).toHaveBeenCalledExactlyOnceWith('usage_stats.no_data', 6, 0);
});

it('preserves its canvas, selection and tooltip during a background refresh', async () => {
  await render();
  await hover(1);
  const canvas = container.querySelector('canvas');
  const plugin = captured.plugins[0];
  await render(speed, true);
  expect(container.querySelector('canvas')).toBe(canvas);
  expect(container.querySelector<HTMLButtonElement>('button')!.disabled).toBe(false);
  expect(document.querySelector('[data-model-speed-tooltip]')).not.toBeNull();
  expect(container.textContent).not.toContain('common.loading');
  const refreshed = structuredClone(speed);
  refreshed.series[0].second_half.speed_tps_sum = 123;
  await render(refreshed);
  expect(container.querySelector('canvas')).toBe(canvas);
  await hover(1, plugin);
  expect(document.querySelector('[data-model-speed-tooltip]')!.textContent).toContain('123.0 t/s');
});

it('shows no data instead of an impossible selection prompt for an empty available window', async () => {
  await render({ available: true, series: [] });
  expect(container.textContent).toContain('usage_stats.no_data');
  expect(container.textContent).not.toContain('usage_stats.model_speed_empty_selection');
});


it('highlights only the pointed half and clears emphasis over the connector or a missing half', async () => {
  await render();
  const highlighted = () => document.querySelector('[data-model-speed-highlighted]')?.textContent;
  await hover(1, captured.plugins[0], 'mousemove', 40);
  expect(highlighted()).toContain('usage_stats.model_speed_first_half');
  await hover(1, captured.plugins[0], 'mousemove', 80);
  expect(highlighted()).toContain('usage_stats.model_speed_second_half');
  await hover(1, captured.plugins[0], 'mousemove', 60);
  expect(highlighted()).toBeUndefined();
  expect(document.querySelectorAll('[data-model-speed-tooltip] li')).toHaveLength(2);
  await hover(2, captured.plugins[0], 'mousemove', 40);
  expect(highlighted()).toBeUndefined();
  await hover(2, captured.plugins[0], 'mousemove', 80);
  expect(highlighted()).toContain('usage_stats.model_speed_second_half');
});
