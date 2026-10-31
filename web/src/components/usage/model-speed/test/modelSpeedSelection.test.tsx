// @vitest-environment happy-dom
import { act, useEffect } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { modelSpeedStorageKey, useModelSpeedSelection, type ModelSpeedCandidate, type ModelSpeedView } from '../modelSpeedSelection';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
let latest: ReturnType<typeof useModelSpeedSelection>;
function Harness({ candidates, scope = 'admin', view = 'analysis', queryKey = 'query-a', ready = true }: { candidates: ModelSpeedCandidate[]; scope?: string | null; view?: ModelSpeedView; queryKey?: string; ready?: boolean }) {
  const result = useModelSpeedSelection(view, scope, candidates, queryKey, ready);
  useEffect(() => { latest = result; }, [result]);
  return null;
}
const candidates = [
  { model: 'b', total_tokens: 1000 }, { model: 'a', total_tokens: 1000 },
  { model: 'c', total_tokens: 300 }, { model: 'd', total_tokens: 200 },
  { model: 'e', total_tokens: 100 }, { model: 'fast', total_tokens: 1 },
];

describe('model speed selection preferences', () => {
  let container: HTMLDivElement;
  let root: Root;
  beforeEach(() => { localStorage.clear(); container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
  const render = async (items: ModelSpeedCandidate[], scope: string | null = 'admin', view: ModelSpeedView = 'analysis', queryKey = 'query-a', ready = true) => {
    await act(async () => root.render(<Harness candidates={items} scope={scope} view={view} queryKey={queryKey} ready={ready} />));
  };

  it('initializes once when data arrives, orders by total tokens and uses the current ranking on reset', async () => {
    await render([]);
    expect(localStorage.length).toBe(0);
    await render(candidates);
    expect(latest.selected).toEqual(['a', 'b', 'c', 'd', 'e']);
    const colors = latest.colors;
    const next = [{ model: 'fast', total_tokens: 9000 }, ...candidates];
    await render(next);
    expect(latest.selected).toEqual(['a', 'b', 'c', 'd', 'e']);
    expect(latest.colors).toEqual(colors);
    await act(async () => latest.reset());
    expect(latest.selected).toEqual(['fast', 'a', 'b', 'c', 'd']);
  });

  it('keeps missing models, explicit empty selection, and stable colors through updates and remounts', async () => {
    await render(candidates);
    const bColor = latest.colors.b;
    await act(async () => latest.choose(['b', 'missing']));
    expect(latest.colors.b).toBe(bColor);
    await render([{ model: 'new', total_tokens: 99999 }]);
    expect(latest.selected).toEqual(['b', 'missing']);
    await act(async () => root.unmount());
    root = createRoot(container);
    await render(candidates);
    expect(latest.selected).toEqual(['b', 'missing']);
    expect(latest.colors.b).toBe(bColor);
    await act(async () => latest.choose([]));
    await act(async () => root.unmount());
    root = createRoot(container);
    await render(candidates);
    expect(latest.selected).toEqual([]);
    expect(JSON.parse(localStorage.getItem(modelSpeedStorageKey('analysis', 'admin'))!).models).toEqual([]);
  });

  it('isolates Analysis, Realtime, admin and each key viewer', async () => {
    await render(candidates);
    await act(async () => latest.choose(['custom']));
    await render(candidates, 'key:42');
    expect(latest.selected).toEqual(['a', 'b', 'c', 'd', 'e']);
    await act(async () => latest.choose([]));
    await render(candidates, 'key:99');
    expect(latest.selected.length).toBe(5);
    await render(candidates, 'admin', 'realtime');
    expect(latest.selected.length).toBe(5);
    await render(candidates);
    expect(latest.selected).toEqual(['custom']);
    await render(candidates, 'key:42');
    expect(latest.selected).toEqual([]);
  });

  it('keeps legacy viewer selections temporary when no stable id is available', async () => {
    await render(candidates, null);
    expect(latest.selected.length).toBe(5);
    await act(async () => latest.choose(['temporary']));
    expect(latest.selected).toEqual(['temporary']);
    expect(localStorage.length).toBe(0);
  });

  it('recovers malformed storage and limits saved selections without discarding absent models', async () => {
    localStorage.setItem(modelSpeedStorageKey('analysis', 'admin'), '{broken');
    await render(candidates);
    expect(latest.selected.length).toBe(5);
    localStorage.setItem(modelSpeedStorageKey('analysis', 'key:42'), JSON.stringify({ version: 1, models: ['unknown', 'unknown', 'one', 'two', 'three', 'four', 'five'] }));
    await render(candidates, 'key:42');
    expect(latest.selected).toEqual(['unknown', 'one', 'two', 'three', 'four']);
  });

  it('reselects auto defaults only for a new query after its current response arrives', async () => {
    await render(candidates);
    expect(latest.mode).toBe('auto');
    const next = [{ model: 'new', total_tokens: 9000 }, ...candidates];
    await render(next);
    expect(latest.selected).toEqual(['a', 'b', 'c', 'd', 'e']);
    await render(candidates, 'admin', 'analysis', 'query-b', false);
    expect(latest.selected).toEqual([]);
    await render(next, 'admin', 'analysis', 'query-b');
    expect(latest.selected).toEqual(['new', 'a', 'b', 'c', 'd']);
    expect(JSON.parse(localStorage.getItem(modelSpeedStorageKey('analysis', 'admin'))!).mode).toBe('auto');
  });

  it('initializes auto after an empty query later receives its first candidates', async () => {
    await render(candidates);
    await render([], 'admin', 'analysis', 'empty-query');
    expect(latest.selected).toEqual([]);
    await render([{ model: 'arrived', total_tokens: 10 }], 'admin', 'analysis', 'empty-query');
    expect(latest.selected).toEqual(['arrived']);
  });

  it('keeps manual and legacy selections across queries until reset restores auto mode', async () => {
    localStorage.setItem(modelSpeedStorageKey('analysis', 'admin'), JSON.stringify({ version: 1, models: ['legacy'] }));
    await render(candidates);
    expect(latest.mode).toBe('manual');
    await render(candidates, 'admin', 'analysis', 'query-b');
    expect(latest.selected).toEqual(['legacy']);
    await act(async () => latest.choose([]));
    await render(candidates, 'admin', 'analysis', 'query-c');
    expect(latest.selected).toEqual([]);
    expect(latest.mode).toBe('manual');
    await act(async () => latest.reset());
    expect(latest.mode).toBe('auto');
    expect(latest.selected).toEqual(['a', 'b', 'c', 'd', 'e']);
    await render([{ model: 'next-query', total_tokens: 1 }], 'admin', 'analysis', 'query-d');
    expect(latest.selected).toEqual(['next-query']);
  });
});
