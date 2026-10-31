// @vitest-environment happy-dom
import { act, type ComponentProps } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { AnalysisPanel } from '../AnalysisPanel';
import { emptyAnalysis } from './analysisFixtures';

let speedProps: ComponentProps<typeof import('../../model-speed/AnalysisModelSpeedCard').AnalysisModelSpeedCard>;
vi.mock('../../model-speed/AnalysisModelSpeedCard', () => ({ AnalysisModelSpeedCard: (props: typeof speedProps) => { speedProps = props; return <section data-model-speed-card>Speed</section>; } }));
vi.mock('react-chartjs-2', () => ({ Bar: () => <div />, Scatter: () => <div />, Doughnut: () => <div /> }));
vi.mock('@/i18n', () => ({ default: { language: 'en', t: (key: string) => key } }));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
beforeEach(() => { container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

it('places full-width latency before a speed/efficiency row and forwards the same analysis response', async () => {
  const speed = { buckets: [{ start: '2026-10-08T00:00:00+08:00', end: '2026-10-08T03:00:00+08:00' }], series: [] };
  await act(async () => root.render(<AnalysisPanel analysis={{ ...emptyAnalysis, granularity: 'daily', timezone: 'Asia/Shanghai', model_speed: speed }} loading={false} isDark isMobile={false} modelSpeedQueryKey="range:key42" />));
  const latency = Array.from(container.querySelectorAll('section')).find((section) => section.textContent?.includes('usage_stats.analysis_latency_title'))!;
  const speedCard = container.querySelector('[data-model-speed-card]')!;
  expect(latency.parentElement).toBe(container.firstElementChild);
  expect(speedCard.parentElement?.previousElementSibling).toBe(latency);
  expect(speedCard.nextElementSibling?.textContent).toContain('usage_stats.analysis_model_efficiency_title');
  expect(speedProps.speed).toBe(speed);
  expect(speedProps.timezone).toBe('Asia/Shanghai');
  expect(speedProps.granularity).toBe('daily');
  expect(speedProps.preferenceScope).toBe('admin');
  expect(speedProps.queryKey).toBe('range:key42');
});

it('forwards key viewer preference scope and the loading state through the shared panel', async () => {
  await act(async () => root.render(<AnalysisPanel analysis={null} loading isDark={false} isMobile compositionDimensions={['model']} modelSpeedPreferenceScope="key-viewer:42" />));
  expect(speedProps.preferenceScope).toBe('key-viewer:42');
  expect(speedProps.loading).toBe(true);
  expect(speedProps.isMobile).toBe(true);
  expect(speedProps.speed).toBeUndefined();
});
