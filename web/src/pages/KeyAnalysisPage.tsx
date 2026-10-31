import { useChartRefresh } from '@/components/usage/charts/chartLifecycle';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, fetchKeyAnalysis, fetchKeyAnalysisLatency, isCostsBusy, isUsageRangeBoundsConflict } from '@/lib/api';
import type { AnalysisLatencyDiagnostics, AnalysisResponse, AuthSessionAPIKeySummary, UsageCustomRange, UsageTimeRange } from '@/lib/types';
import { AnalysisPanel, TimeRangeControl } from '@/components/usage';
import type { ModelSpeedRefreshSignal } from '@/components/usage/model-speed/modelSpeedAnimation';
import { KeyViewerShell } from '@/features/key-viewer/KeyViewerShell';
import type { KeyViewerPath } from '@/features/key-viewer/navigation';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { buildUsageStatsQueryKey, useThemeStore } from '@/stores';
import {
  clampStoredUsageRangeStateToCurrentBounds,
  resolveUsageRangeRecoveryTimeZone,
  type StoredUsageRangeState,
} from '@/utils/usage/customRange';
import { buildUsageRangeQuery } from '@/utils/usage/rangeQuery';
import { getAnalysisPricingSnapshot } from '@/utils/usage/pricingSnapshot';
import { Button } from '@/components/ui/Button';
import { loadKeyViewerTimeRange, persistKeyViewerTimeRange } from '@/features/key-viewer/timeRange';
import styles from '@/features/key-viewer/KeyViewerShell.module.scss';

const loadTimeRange = (): StoredUsageRangeState => {
  return loadKeyViewerTimeRange();
};

export interface KeyAnalysisPageProps {
  apiKey?: AuthSessionAPIKeySummary;
  onNavigate: (path: KeyViewerPath) => void;
  onAuthRequired?: () => void;
}

export function KeyAnalysisPage({ apiKey, onNavigate, onAuthRequired }: KeyAnalysisPageProps) {
  const { t } = useTranslation();
  const isMobile = useMediaQuery('(max-width: 768px)');
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const isDark = resolvedTheme === 'dark';
  const [timeRangeState, setTimeRangeState] = useState<StoredUsageRangeState>(loadTimeRange);
  const { range: timeRange, customRange } = timeRangeState;
  const [analysis, setAnalysis] = useState<AnalysisResponse | null>(null);
  const [analysisLoading, setAnalysisLoading] = useState(false);
  const [analysisError, setAnalysisError] = useState('');
  const [latency, setLatency] = useState<AnalysisLatencyDiagnostics | null>(null);
  const [latencyLoading, setLatencyLoading] = useState(false);
  const [latencyError, setLatencyError] = useState('');
  const [manualRefreshLoading, setManualRefreshLoading] = useState(false);
  const [chartRefresh, markChartLoaded] = useChartRefresh();
  const [latencyRefresh, markLatencyLoaded] = useChartRefresh();
  const loadedLatencyScopeRef = useRef<string | null>(null);
  const [modelSpeedRefresh, setModelSpeedRefresh] = useState<ModelSpeedRefreshSignal>();
  const requestControllerRef = useRef<AbortController | null>(null);
  const loadedAnalysisScopeRef = useRef<{ rangeKey: string; apiKey?: AuthSessionAPIKeySummary } | null>(null);
  const analysisTimeZoneRef = useRef(timeRangeState.timeZone);
  const usageRangeQuery = useMemo(() => buildUsageRangeQuery({
    range: timeRange,
    customUnit: customRange?.unit,
    customStart: customRange?.start,
    customEnd: customRange?.end,
  }), [customRange?.end, customRange?.start, customRange?.unit, timeRange]);
  const rangeKey = usageRangeQuery.valid ? buildUsageStatsQueryKey(usageRangeQuery) : null;
  // 同范围、同 Viewer 的刷新可保留已展示金额；切换范围或账号时立即隐藏旧结果。
  const currentAnalysis = rangeKey && loadedAnalysisScopeRef.current?.rangeKey === rangeKey && loadedAnalysisScopeRef.current.apiKey === apiKey ? analysis : null;
  const snapshotMismatch = !analysisLoading && getAnalysisPricingSnapshot(currentAnalysis).mixed;
  const rangeTimeZone = currentAnalysis?.timezone ?? timeRangeState.timeZone;

  const recoverRangeBoundsConflict = useCallback((error: unknown) => {
    if (!isUsageRangeBoundsConflict(error)) return false;
    const timeZone = resolveUsageRangeRecoveryTimeZone(timeRangeState, analysisTimeZoneRef.current)?.trim();
    if (!timeZone) return false;
    const nextState = clampStoredUsageRangeStateToCurrentBounds(timeRangeState, {
      nowMs: Date.now(),
      timeZone,
    });
    if (nextState === timeRangeState) return false;
    setTimeRangeState(nextState);
    return true;
  }, [timeRangeState]);

  const handleTimeRangeChange = useCallback((range: UsageTimeRange, nextCustomRange?: UsageCustomRange) => {
    let nextState: StoredUsageRangeState;
    if (range === 'custom' && nextCustomRange) {
      nextState = { range, customRange: nextCustomRange, timeZone: rangeTimeZone };
    } else {
      nextState = { ...timeRangeState, range };
    }
    setTimeRangeState(nextState);
    // 切换页面可能紧接着发生，先同步写入共享缓存，再等待状态 effect。
    persistKeyViewerTimeRange(nextState);
  }, [rangeTimeZone, timeRangeState]);

  const loadAnalysis = useCallback(async (replaySpeed = false) => {
    if (!usageRangeQuery.valid) return;
    requestControllerRef.current?.abort();
    const controller = new AbortController();
    requestControllerRef.current = controller;
    setAnalysisLoading(true);
    setAnalysisError('');
    setLatencyLoading(true);
    setLatencyError('');
    const scope = JSON.stringify([rangeKey, apiKey?.id]);
    const sameLatencyScope = loadedLatencyScopeRef.current === scope;
    if (!sameLatencyScope) setLatency(null);

    // 主 Analysis 与 Latency 同时加载，但分别更新状态，避免一个慢请求阻塞另一块内容。
    const coreRequest = fetchKeyAnalysis(usageRangeQuery, controller.signal).then((response) => {
      if (controller.signal.aborted || requestControllerRef.current !== controller) return;
      // 项目时区只作为后续 409 恢复依据，不参与当前请求 callback 身份，避免响应触发重复加载。
      analysisTimeZoneRef.current = response.timezone;
      loadedAnalysisScopeRef.current = { rangeKey: rangeKey!, apiKey };
      setAnalysis(response);
      if (replaySpeed) markChartLoaded(response);
      const speed = response.model_speed;
      if (replaySpeed && speed) setModelSpeedRefresh((previous) => ({ source: speed, revision: (previous?.revision ?? 0) + 1 }));
      setAnalysisLoading(false);
    }, (error: unknown) => {
      if (controller.signal.aborted || requestControllerRef.current !== controller) return;
      setAnalysisLoading(false);
      if (recoverRangeBoundsConflict(error)) return;
      if (error instanceof ApiError && error.status === 401) {
        onAuthRequired?.();
        return;
      }
      setAnalysisError(isCostsBusy(error) ? 'COSTS_BUSY' : 'KEY_ANALYSIS_LOAD_FAILED');
    });
    const latencyRequest = fetchKeyAnalysisLatency(usageRangeQuery, controller.signal).then((response) => {
      if (requestControllerRef.current !== controller) return;
      loadedLatencyScopeRef.current = scope;
      setLatency(response);
      if (replaySpeed) markLatencyLoaded(response);
      setLatencyLoading(false);
    }, (error: unknown) => {
      if (controller.signal.aborted || requestControllerRef.current !== controller) return;
      setLatencyLoading(false);
      if (recoverRangeBoundsConflict(error)) return;
      if (error instanceof ApiError && error.status === 401) {
        onAuthRequired?.();
        return;
      }
      setLatencyError('KEY_ANALYSIS_LATENCY_LOAD_FAILED');
    });
    await Promise.all([coreRequest, latencyRequest]);
    if (requestControllerRef.current === controller) {
      requestControllerRef.current = null;
    }
  }, [apiKey, onAuthRequired, rangeKey, recoverRangeBoundsConflict, usageRangeQuery, markChartLoaded, markLatencyLoaded]);

  useEffect(() => {
    void loadAnalysis();
    return () => {
      requestControllerRef.current?.abort();
      requestControllerRef.current = null;
    };
  }, [loadAnalysis]);

  useEffect(() => {
    persistKeyViewerTimeRange(timeRangeState);
  }, [timeRangeState]);

  const handleManualRefresh = useCallback(async () => {
    if (manualRefreshLoading) return;
    setManualRefreshLoading(true);
    try {
      await loadAnalysis(true);
    } finally {
      setManualRefreshLoading(false);
    }
  }, [loadAnalysis, manualRefreshLoading]);

  const displayAnalysisError = analysisError === 'COSTS_BUSY'
    ? t('key_analysis.costs_busy')
    : analysisError ? t('key_analysis.load_failed') : '';
  const displayLatencyError = latencyError ? t('key_analysis.latency_load_failed') : '';
  const panelLoading = (analysisLoading && !currentAnalysis) || (analysisError === 'COSTS_BUSY' && !currentAnalysis);

  return (
    <KeyViewerShell
      activePage="analysis"
      apiKey={apiKey}
      loading={analysisLoading && !currentAnalysis}
      filters={[<TimeRangeControl key="range" value={timeRange} customRange={customRange} timeZone={rangeTimeZone} onChange={handleTimeRangeChange} ariaLabel={t('usage_stats.range_filter')} labelInsideTrigger />]}
      onRefresh={() => void handleManualRefresh()}
      refreshing={manualRefreshLoading}
      onNavigate={onNavigate}
      onAuthRequired={onAuthRequired}
    >
      {displayAnalysisError && (
        <div className={`${styles.errorBox} ${styles.errorBoxWithAction}`} role="alert">
          <span>{displayAnalysisError}</span>
          <button type="button" className={styles.errorRetryButton} onClick={() => void handleManualRefresh()}>
            {t('common.retry')}
          </button>
        </div>
      )}
      {snapshotMismatch && <div className={styles.errorBox} role="status" data-pricing-snapshot-notice>
        {t('cost_estimates.snapshot_mismatch')}
        <Button type="button" variant="secondary" size="sm" disabled={manualRefreshLoading || analysisLoading} onClick={() => void handleManualRefresh()}>
          {t('cost_estimates.refresh')}
        </Button>
      </div>}
      <AnalysisPanel
        analysis={currentAnalysis}
        loading={panelLoading}
        latencyDiagnostics={loadedLatencyScopeRef.current === JSON.stringify([rangeKey, apiKey?.id]) ? latency : null}
        latencyLoading={latencyLoading}
        latencyError={displayLatencyError}
        isDark={isDark}
        isMobile={isMobile}
        compositionDimensions={['model']}
        modelSpeedPreferenceScope={apiKey?.id ? `key-viewer:${apiKey.id}` : null}
        modelSpeedQueryKey={JSON.stringify([rangeKey, apiKey?.id ?? null])}
        modelSpeedRefreshSignal={modelSpeedRefresh}
        chartRefreshSignal={chartRefresh}
        latencyRefreshSignal={latencyRefresh}
      />
    </KeyViewerShell>
  );
}
