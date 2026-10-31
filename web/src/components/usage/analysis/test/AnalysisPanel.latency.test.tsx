// @vitest-environment happy-dom

import React from 'react';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-chartjs-2', () => ({
  Bar: () => React.createElement('div'),
  Doughnut: () => React.createElement('div'),
  Scatter: () => React.createElement('canvas', { 'data-latency-chart': true }),
}));

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({ t: (key: string) => key }),
}));

import { renderAnalysisPanel } from './analysisFixtures';

describe('AnalysisPanel latency loading boundary', () => {
  const latencyCard = (container: HTMLElement) => [...container.querySelectorAll('h2')].find((heading) => heading.textContent === 'usage_stats.analysis_latency_title')!.closest('section')!;

  it('keeps core cards settled while only the latency card is loading', () => {
    const container = renderAnalysisPanel({ latencyLoading: true });
    expect(latencyCard(container).textContent).toContain('common.loading');
    latencyCard(container).remove();
    expect(container.textContent).not.toContain('common.loading');
  });

  it('shows a latency-only error without replacing the core panel', () => {
    const container = renderAnalysisPanel({ latencyError: 'latency failed' });
    expect(latencyCard(container).textContent).toContain('latency failed');
    expect(container.textContent).toContain('usage_stats.analysis_token_usage_title');
  });

  it('shows recent-range guidance when latency diagnostics are unsupported', () => {
    const container = renderAnalysisPanel({
      latencyDiagnostics: {
        supported: false,
        unsupported_reason: 'range_outside_recent_30_days',
        points: [],
        density: [],
        total_points: 0,
        sampled: false,
        p95_ttft_ms: 0,
        p95_latency_ms: 0,
        max_ttft_ms: 0,
        max_latency_ms: 0,
      },
    });
    expect(latencyCard(container).textContent).toContain('usage_stats.analysis_latency_recent_range_only');
    expect(latencyCard(container).textContent).not.toContain('usage_stats.no_data');
  });
  it.each([{ latencyLoading: true }, { latencyError: 'refresh failed' }])('preserves existing latency data during refresh or failure: %o', (state) => {
    const container = renderAnalysisPanel({ ...state, latencyDiagnostics: {
      supported: true, points: [{ ttft_ms: 100, latency_ms: 1000 }], density: [], total_points: 1,
      sampled: false, p95_ttft_ms: 100, p95_latency_ms: 1000, max_ttft_ms: 100, max_latency_ms: 1000,
    } });
    expect(latencyCard(container).querySelector('canvas')).not.toBeNull();
    expect(latencyCard(container).textContent).not.toContain('common.loading');
    if (state.latencyError) expect(latencyCard(container).textContent).toContain(state.latencyError);
  });

});
