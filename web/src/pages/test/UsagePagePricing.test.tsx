// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { UsagePage } from '../UsagePage';

vi.mock('react-chartjs-2', () => ({
  Bar: () => null, Chart: () => null, Doughnut: () => null, Line: () => null, Scatter: () => null,
}));

const matrix = {
  credentials: [{ directory_id: 1, subject_id: 'cred_test', name: 'Synthetic credential', provider_type: 'openai', auth_type: 'apikey', status: 'active', binding_status: 'bound' }],
  models: ['synthetic/model'], cells: [], credential_page: 1, credential_page_size: 50,
  model_page: 1, model_page_size: 50, total_credentials: 1, total_models: 1, snapshot_id: 'test-snapshot',
};

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });

describe('UsagePage new pricing integration', () => {
  let container: HTMLDivElement;
  let root: Root;
  let requests: string[];

  beforeEach(async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    await i18n.changeLanguage('en');
    localStorage.clear();
    window.history.replaceState(null, '', '/settings');
    requests = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), 'http://localhost').pathname;
      requests.push(path);
      switch (path) {
        case '/api/v1/status': return json({ timezone: 'UTC' });
        case '/api/v1/version': return json({ version: 'test' });
        case '/api/v1/usage/api-keys/options': return json({ options: [] });
        case '/api/v1/usage/api-keys/settings': return json({ items: [] });
        case '/api/v1/auth/sessions': return json({ sessions: [] });
        case '/api/v1/pricing/credential-model-matrix': return json(matrix);
        case '/api/v1/pricing/credentials/sync-status': return json({ state: 'success', in_flight: false });
        case '/api/v1/pricing/official': return json({ pricing: [] });
        case '/api/v1/pricing/stored/models': return json({ models: [], config_revision: 0 });
        case '/api/v1/pricing/stored/model-options': return json({ models: [] });
        case '/api/v1/pricing/stored/recalculations/current': return json(null);
        default: return json({ error: 'Synthetic unavailable response' }, 404);
      }
    }));
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    localStorage.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('keeps matrix and official entry points alongside separate stored valuation, without retired APIs', async () => {
    await act(async () => root.render(<UsagePage />));
    expect(container.textContent).toContain(i18n.t('title', { ns: 'matrix_pricing' }));
    expect(container.textContent).toContain(i18n.t('official_title', { ns: 'matrix_pricing' }));
    for (const key of ['pricing_identity_migration', 'pricing_credential_defaults', 'pricing_channels', 'pricing_credential_models', 'pricing_channel_models', 'pricing_credentials']) {
      expect(container.textContent).not.toContain(i18n.t(`${key}.title`));
    }
    const pricingPaths = requests.filter((path) => path.startsWith('/api/v1/pricing'));
    expect(pricingPaths).toContain('/api/v1/pricing/credential-model-matrix');
    expect(pricingPaths).toContain('/api/v1/pricing/official');
    expect(container.textContent).toContain('Stored valuation / 已存估值');
    expect(pricingPaths).toContain('/api/v1/pricing/stored/models');
    expect(pricingPaths.every((path) => ['/api/v1/pricing/credential-model-matrix', '/api/v1/pricing/official', '/api/v1/pricing/credentials/sync-status', '/api/v1/pricing/stored/models', '/api/v1/pricing/stored/model-options', '/api/v1/pricing/stored/recalculations/current'].includes(path))).toBe(true);
    expect(requests).not.toContain('/api/v1/models');
  });
});
