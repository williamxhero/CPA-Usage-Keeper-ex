import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { RealtimeTokenShareRibbons } from '../RealtimeTokenShareRibbons';
import type { DualCosts } from '@/lib/types';
import i18n from '@/i18n';

describe('RealtimeTokenShareRibbons', () => {
  it('renders backend known zero and unavailable reference while synthetic Other stays explicitly absent', async () => {
    await i18n.changeLanguage('en');
    const dualCosts: DualCosts = {
      configured: { total_cost_usd: 0, uncached_input_cost_usd: 0, output_cost_usd: 0, cache_read_cost_usd: 0, cache_write_cost_usd: 0, has_known: true, complete: true, status: 'complete' },
      reference: { total_cost_usd: null, uncached_input_cost_usd: 0, output_cost_usd: 0, cache_read_cost_usd: 0, cache_write_cost_usd: 0, has_known: false, complete: false, status: 'unavailable', unavailable_reason: 'missing_baseline' },
    };
    const html = renderToStaticMarkup(<RealtimeTokenShareRibbons loading={false} items={[
      { key: 'known', label: 'Known', tokens: 1, requests: 1, share: 100, cost: 99, dual_costs: dualCosts },
      ...['a', 'b', 'c', 'd'].map(key => ({ key, label: key, tokens: 0, requests: 0, share: 0 })),
      { key: '__realtime_others__', label: 'Other', tokens: 0, requests: 0, share: 0, cost: 12 },
    ]} />);
    expect(html).toContain('>$0.0000</strong>');
    expect(html).toContain('Known zero');
    expect(html).toContain('Baseline price is missing');
    expect(html).not.toContain('$99.00');
    const other = html.slice(html.indexOf('data-ribbon-row="5"'));
    expect(other).toContain('>$12.00</strong>');
    expect(other).toContain('data-cost-estimate="reference"');
    expect(other).toContain('>—</strong>');
    expect(other).toContain('Not provided by this server');
    expect(other).not.toContain('Known zero');
    expect(other).not.toContain('>Complete</small>');
  });

  it('keeps complete labels, the exact small share, unknown cost, and synthetic Other separate', () => {
    const label = 'very-long-credential-name-without-any-spaces.json';
    const html = renderToStaticMarkup(
      <RealtimeTokenShareRibbons loading={false} items={[
        { key: '__realtime_others__', label, tokens: 997, requests: 7, share: 99.7, cost: 1 },
        { key: 'tiny', label: 'tiny', tokens: 3, requests: 1, share: 0.3, cost: null },
        { key: 'b', label: 'b', tokens: 0, requests: 0, share: 0 },
        { key: 'c', label: 'c', tokens: 0, requests: 0, share: 0 },
        { key: 'd', label: 'd', tokens: 0, requests: 0, share: 0 },
        { key: '__realtime_others__', label: 'Other', tokens: 0, requests: 0, share: 0 },
      ]} />,
    );
    expect(html).toContain(label);
    expect(html).toContain('0.30%');
    expect(html).toContain('99.70%');
    expect(html).toContain('data-ribbon-row="5"');
    expect(html).toContain('data-ribbon-row="0"');
    expect(html).toContain('>—</strong>');
    expect(html).toContain('data-cost-estimate="reference"');
    expect(html).toContain('Not provided by this server');
  });
});
