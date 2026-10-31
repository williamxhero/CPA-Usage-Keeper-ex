// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from 'vitest';
import { fetchKeyOverviewRealtime, fetchUsageOverviewRealtime } from '../api';

const speed = {
  available: true, start: '2026-10-08T12:00:00+08:00', mid: '2026-10-08T12:07:30+08:00', end: '2026-10-08T12:15:00+08:00',
  series: [{ model: 'all-models', total_tokens: 100, speed_sample_count: 1,
    first_half: { speed_tps_sum: 0, speed_sample_count: 0, mean_tps: null },
    second_half: { speed_tps_sum: 30, speed_sample_count: 1, mean_tps: 30 },
  }],
};
afterEach(() => vi.restoreAllMocks());

it.each([fetchUsageOverviewRealtime, fetchKeyOverviewRealtime])('preserves cached model speed through the existing normalized fetch', async (fetchRealtime) => {
  const request = vi.spyOn(globalThis, 'fetch').mockResolvedValue(Response.json({ model_speed: speed }));
  const response = await fetchRealtime();
  expect(request).toHaveBeenCalledTimes(1);
  expect(response.model_speed).toEqual(speed);
});

it.each([fetchUsageOverviewRealtime, fetchKeyOverviewRealtime])('normalizes older responses as unavailable speed with empty models', async (fetchRealtime) => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(Response.json({ window: '15m' }));
  expect((await fetchRealtime()).model_speed).toEqual({ available: false, series: [], start: undefined, mid: undefined, end: undefined });
});
