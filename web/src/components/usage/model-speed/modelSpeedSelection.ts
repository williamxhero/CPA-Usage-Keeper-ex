import { useCallback, useEffect, useMemo, useState } from 'react';

export const MODEL_SPEED_SELECTION_LIMIT = 5;
export type ModelSpeedCandidate = { model: string; total_tokens: number };
export type ModelSpeedView = 'analysis' | 'realtime';
type SelectionMode = 'auto' | 'manual';
type Selection = { mode: SelectionMode; models: string[]; colors: Record<string, number> };

export const modelSpeedStorageKey = (view: ModelSpeedView, scope: string) =>
  `cpa-usage-keeper-model-speed-v1:${view}:${scope}`;

export function sortModelSpeedCandidates(candidates: readonly ModelSpeedCandidate[]) {
  return [...candidates].sort((left, right) => right.total_tokens - left.total_tokens
    || (left.model < right.model ? -1 : left.model > right.model ? 1 : 0));
}

function loadSelection(key: string | null): Selection | null {
  if (key === null) return null;
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== 'object' || !('version' in value) || value.version !== 1
      || !('models' in value) || !Array.isArray(value.models)
      || !value.models.every((model) => typeof model === 'string' && model.trim())) return null;
    const models = [...new Set(value.models as string[])].slice(0, MODEL_SPEED_SELECTION_LIMIT);
    const saved: Record<string, number> = {};
    if ('colors' in value && value.colors && typeof value.colors === 'object') {
      for (const [model, index] of Object.entries(value.colors)) {
        if (typeof index === 'number' && Number.isInteger(index) && index >= 0 && index < MODEL_SPEED_SELECTION_LIMIT) saved[model] = index;
      }
    }
    // 旧记录没有模式，无法判断是默认还是手选；保守保留为手选，恢复默认时再切到 auto。
    return { mode: 'mode' in value && value.mode === 'auto' ? 'auto' : 'manual', models, colors: assignColors(models, saved) };
  } catch { return null; }
}

function persistSelection(key: string | null, mode: SelectionMode, models: readonly string[], colors: Readonly<Record<string, number>>) {
  if (key === null) return;
  try { localStorage.setItem(key, JSON.stringify({ version: 1, mode, models, colors })); }
  catch { /* 浏览器存储受限时继续保留本次选择。 */ }
}

function assignColors(models: readonly string[], previous: Readonly<Record<string, number>> = {}) {
  const colors: Record<string, number> = {};
  const used = new Set<number>();
  for (const model of models) {
    if (Object.hasOwn(previous, model) && !used.has(previous[model])) { colors[model] = previous[model]; used.add(previous[model]); }
  }
  for (const model of models) {
    if (Object.hasOwn(colors, model)) continue;
    const available = Array.from({ length: MODEL_SPEED_SELECTION_LIMIT }, (_, index) => index).find((index) => !used.has(index)) ?? 0;
    colors[model] = available;
    used.add(available);
  }
  return colors;
}

export function useModelSpeedSelection(view: ModelSpeedView, scope: string | null, candidates: readonly ModelSpeedCandidate[], queryKey = '', ready = true) {
  const storageKey = scope === null ? null : modelSpeedStorageKey(view, scope);
  const sortedCandidates = useMemo(() => sortModelSpeedCandidates(candidates), [candidates]);
  const defaults = useMemo(() => sortedCandidates.slice(0, MODEL_SPEED_SELECTION_LIMIT).map((item) => item.model), [sortedCandidates]);
  const loadState = () => {
    const saved = loadSelection(storageKey);
    const mode = saved?.mode ?? 'auto';
    return { key: storageKey, queryKey, mode, models: mode === 'manual' ? saved!.models : null, colors: saved?.colors ?? {} };
  };
  const [state, setState] = useState(loadState);
  // auto 在新条件的有效响应到达后只初始化一次；空响应继续等待，手选与显式空选不受条件变化影响。
  if (state.key !== storageKey) {
    setState(loadState());
  } else if (state.mode === 'auto' && state.queryKey !== queryKey) {
    setState({ ...state, queryKey, models: null });
  } else if (state.mode === 'auto' && state.models === null && ready && defaults.length > 0) {
    setState({ ...state, models: defaults, colors: assignColors(defaults, state.colors) });
  }
  useEffect(() => {
    if (state.key === storageKey && state.models !== null) persistSelection(storageKey, state.mode, state.models, state.colors);
  }, [state, storageKey]);

  const choose = useCallback((models: string[]) => {
    const next = [...new Set(models)].slice(0, MODEL_SPEED_SELECTION_LIMIT);
    const colors = assignColors(next, state.key === storageKey ? state.colors : {});
    persistSelection(storageKey, 'manual', next, colors);
    setState({ key: storageKey, queryKey, mode: 'manual', models: next, colors });
  }, [state.colors, state.key, storageKey, queryKey]);
  const reset = useCallback(() => {
    const next = ready && defaults.length > 0 ? defaults : null;
    const colors = assignColors(next ?? [], state.key === storageKey ? state.colors : {});
    persistSelection(storageKey, 'auto', next ?? [], colors);
    setState({ key: storageKey, queryKey, mode: 'auto', models: next, colors });
  }, [defaults, ready, state.colors, state.key, storageKey, queryKey]);

  return { selected: state.models ?? [], mode: state.mode, colors: state.colors, candidates: sortedCandidates, choose, reset };
}
