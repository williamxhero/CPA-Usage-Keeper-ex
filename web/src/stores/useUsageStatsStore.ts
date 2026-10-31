import { create } from 'zustand';
import { ApiError, fetchUsageOverview, fetchUsageOverviewRealtime } from '@/lib/api';
import type { OverviewRealtimeBlock, OverviewRealtimeWindow, UsageCustomRangeUnit, UsageOverviewResponse, UsageRangeRequest, UsageTimeRange } from '@/lib/types';

export const USAGE_STATS_STALE_TIME_MS = 60_000;

interface LoadUsageStatsOptions {
  onLoaded?: (response: UsageOverviewResponse) => void;
  force?: boolean;
  staleTimeMs?: number;
  range?: UsageTimeRange;
  unit?: UsageCustomRangeUnit;
  start?: string;
  end?: string;
  apiKeyId?: string;
}

interface LoadUsageStatsRealtimeOptions {
  force?: boolean;
  staleTimeMs?: number;
  apiKeyId?: string;
  realtimeWindow?: OverviewRealtimeWindow;
  onLoaded?: (response: OverviewRealtimeBlock) => void;
}

interface UsageStatsState {
  usage: UsageOverviewResponse | null;
  realtime: OverviewRealtimeBlock | null;
  loading: boolean;
  realtimeLoading: boolean;
  error: string;
  realtimeError: string;
  lastRefreshedAt: number | null;
  lastRealtimeRefreshedAt: number | null;
  lastQueryKey: string | null;
  lastRealtimeQueryKey: string | null;
  lastRealtimeErrorQueryKey: string | null;
  loadUsageStats: (options?: LoadUsageStatsOptions) => Promise<void>;
  loadUsageStatsRealtime: (options?: LoadUsageStatsRealtimeOptions) => Promise<void>;
  clearUsageStats: () => void;
}

let activeOverviewRequest: Promise<void> | null = null;
let activeOverviewRequestKey: string | null = null;
let activeOverviewRequestController: AbortController | null = null;
let activeRealtimeRequest: Promise<void> | null = null;
let activeRealtimeRequestKey: string | null = null;
let activeRealtimeRequestController: AbortController | null = null;

export const buildUsageStatsQueryKey = (request: UsageRangeRequest, apiKeyId?: string): string =>
  `${request.range}:${request.unit ?? ''}:${request.start ?? ''}:${request.end ?? ''}:${apiKeyId ?? ''}`;

const buildRealtimeQueryKey = (apiKeyId?: string, realtimeWindow?: OverviewRealtimeWindow): string =>
  `${apiKeyId ?? ''}:${realtimeWindow ?? ''}`;

export const useUsageStatsStore = create<UsageStatsState>((set, get) => ({
  usage: null,
  realtime: null,
  loading: false,
  realtimeLoading: false,
  error: '',
  realtimeError: '',
  lastRefreshedAt: null,
  lastRealtimeRefreshedAt: null,
  lastQueryKey: null,
  lastRealtimeQueryKey: null,
  lastRealtimeErrorQueryKey: null,
  loadUsageStats: async (options = {}) => {
    const {
      force = false,
      staleTimeMs = USAGE_STATS_STALE_TIME_MS,
      range = '8h',
      onLoaded,
      unit,
      start,
      end,
      apiKeyId,
    } = options;
    const { lastRefreshedAt, loading, usage, lastQueryKey } = get();
    const now = Date.now();
    const request: UsageRangeRequest = { range, unit, start, end };
    const queryKey = buildUsageStatsQueryKey(request, apiKeyId);
    const overviewFresh = Boolean(!force && usage && lastRefreshedAt && lastQueryKey === queryKey && now - lastRefreshedAt < staleTimeMs);

    if (overviewFresh) {
      return;
    }

    if (loading && activeOverviewRequest) {
      // 显式刷新取消旧请求，不复用价格变更前的在途快照。
      if (!force && activeOverviewRequestKey === queryKey) {
        return activeOverviewRequest;
      }
      activeOverviewRequestController?.abort();
    }

    const controller = new AbortController();
    activeOverviewRequestController = controller;
    activeOverviewRequestKey = queryKey;
    set({
      loading: true,
      error: '',
    });

    activeOverviewRequest = (async () => {
      try {
        const overview = await fetchUsageOverview(request, controller.signal, apiKeyId);
        if (activeOverviewRequestController !== controller) return;
        set({
          usage: overview,
          loading: false,
          error: '',
          lastRefreshedAt: Date.now(),
          lastQueryKey: queryKey,
        });
        onLoaded?.(overview);
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        const message = error instanceof ApiError && error.status === 401
          ? 'AUTH_REQUIRED'
          : error instanceof Error
            ? error.message
            : 'Failed to load usage overview';
        if (activeOverviewRequestController === controller) {
          set({
            loading: false,
            error: message,
          });
        }
        throw error;
      } finally {
        if (activeOverviewRequestController === controller) {
          activeOverviewRequest = null;
          activeOverviewRequestKey = null;
          activeOverviewRequestController = null;
        }
      }
    })();

    return activeOverviewRequest;
  },
  loadUsageStatsRealtime: async (options = {}) => {
    const {
      force = false,
      staleTimeMs = USAGE_STATS_STALE_TIME_MS,
      apiKeyId,
      realtimeWindow,
      onLoaded,
    } = options;
    const { lastRealtimeRefreshedAt, realtimeLoading, realtime, lastRealtimeQueryKey, realtimeError, lastRealtimeErrorQueryKey } = get();
    const now = Date.now();
    const realtimeQueryKey = buildRealtimeQueryKey(apiKeyId, realtimeWindow);
    const realtimeFresh = Boolean(!force && realtime && lastRealtimeRefreshedAt && lastRealtimeQueryKey === realtimeQueryKey && now - lastRealtimeRefreshedAt < staleTimeMs);

    if (realtimeFresh) {
      if (realtimeError && lastRealtimeErrorQueryKey !== realtimeQueryKey) {
        set({ realtimeError: '', lastRealtimeErrorQueryKey: null });
      }
      return;
    }

    if (realtimeLoading && activeRealtimeRequest) {
      // 与总览保持同一刷新语义；晚响应受 controller 身份检查约束。
      if (!force && activeRealtimeRequestKey === realtimeQueryKey) {
        return activeRealtimeRequest;
      }
      activeRealtimeRequestController?.abort();
    }

    const controller = new AbortController();
    activeRealtimeRequestController = controller;
    activeRealtimeRequestKey = realtimeQueryKey;
    set({
      realtimeLoading: true,
      realtimeError: '',
      lastRealtimeErrorQueryKey: null,
    });

    activeRealtimeRequest = (async () => {
      try {
        const nextRealtime = await fetchUsageOverviewRealtime({
          signal: controller.signal,
          apiKeyId,
          window: realtimeWindow,
        });
        if (activeRealtimeRequestController !== controller) return;
        set({
          realtime: nextRealtime,
          realtimeLoading: false,
          realtimeError: '',
          lastRealtimeErrorQueryKey: null,
          lastRealtimeRefreshedAt: Date.now(),
          lastRealtimeQueryKey: realtimeQueryKey,
        });
        onLoaded?.(nextRealtime);
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        const message = error instanceof ApiError && error.status === 401
          ? 'AUTH_REQUIRED'
          : error instanceof Error
            ? error.message
            : 'Failed to load usage overview realtime';
        if (activeRealtimeRequestController === controller) {
          set({ realtimeLoading: false, realtimeError: message, lastRealtimeErrorQueryKey: realtimeQueryKey });
        }
        if (error instanceof ApiError && error.status === 401) {
          throw error;
        }
      } finally {
        if (activeRealtimeRequestController === controller) {
          activeRealtimeRequest = null;
          activeRealtimeRequestKey = null;
          activeRealtimeRequestController = null;
        }
      }
    })();

    return activeRealtimeRequest;
  },
  clearUsageStats: () => {
    activeOverviewRequestController?.abort();
    activeRealtimeRequestController?.abort();
    activeOverviewRequest = null;
    activeOverviewRequestKey = null;
    activeOverviewRequestController = null;
    activeRealtimeRequest = null;
    activeRealtimeRequestKey = null;
    activeRealtimeRequestController = null;
    set({ usage: null, realtime: null, error: '', realtimeError: '', loading: false, realtimeLoading: false, lastRefreshedAt: null, lastRealtimeRefreshedAt: null, lastQueryKey: null, lastRealtimeQueryKey: null, lastRealtimeErrorQueryKey: null });
  }
}));
