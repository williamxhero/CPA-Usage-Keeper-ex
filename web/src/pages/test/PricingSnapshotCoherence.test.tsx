// @vitest-environment happy-dom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/i18n'
import { requestEventsLiveSnapshot } from './requestEventsLiveFixture'
import { useUsageStatsStore } from '@/stores'
import { KEY_VIEWER_TIME_RANGE_STORAGE_KEY } from '@/features/key-viewer/timeRange'

const api = vi.hoisted(() => ({
  fetchUsageOverview: vi.fn(), fetchKeyOverview: vi.fn(), fetchUsageOverviewComparisons: vi.fn(),
  fetchUsageActivity: vi.fn(), fetchKeyActivity: vi.fn(), fetchUsageEvents: vi.fn(),
  openUsageEventsLive: vi.fn(), fetchUsageOverviewRealtime: vi.fn(), fetchAnalysis: vi.fn(), fetchAnalysisLatency: vi.fn(),
}))
vi.mock('@/lib/api', async (original) => ({
  ...await original<typeof import('@/lib/api')>(), ...api,
  fetchStatus: async () => ({ timezone: 'UTC' }),
  fetchVersion: async () => ({ version: 'test' }),
  fetchCpaApiKeyOptions: async () => ({ options: [{ id: '11', label: 'Team Key' }, { id: '33', label: 'Other Key' }] }),
  fetchUsageEventModelFilterOptions: async () => ({ models: [] }),
  fetchUsageEventSourceFilterOptions: async () => ({ sources: [] }),
}))
vi.mock('react-chartjs-2', () => ({ Bar: () => null, Chart: () => null, Doughnut: () => null, Line: () => null, Scatter: () => null }))
import { UsagePage } from '../UsagePage'
import { KeyOverviewPage } from '../KeyOverviewPage'

const rangeStorage = 'cli-proxy-usage-time-range-v1'
const customRange = JSON.stringify({ range: 'custom', customRange: { unit: 'day', start: '2026-09-01', end: '2026-09-02' }, timeZone: 'UTC' })
const overview = (id?: string) => ({ usage: {}, timezone: 'UTC', pricing_snapshot_id: id })
const comparisons = (id?: string) => ({ models: [], pricing_snapshot_id: id })

for (const viewer of ['admin', 'key'] as const) describe(`${viewer} overview pricing snapshot coherence`, () => {
  let container: HTMLDivElement
  let root: Root
  const overviewAPI = viewer === 'admin' ? api.fetchUsageOverview : api.fetchKeyOverview
  const storageKey = viewer === 'admin' ? rangeStorage : KEY_VIEWER_TIME_RANGE_STORAGE_KEY
  const render = async () => { await act(async () => root.render(viewer === 'admin' ? <UsagePage /> : <KeyOverviewPage onNavigate={() => undefined} />)) }

  beforeEach(async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true
    await i18n.changeLanguage('en')
    localStorage.clear()
    window.history.replaceState(null, '', '/overview')
    useUsageStatsStore.getState().clearUsageStats()
    localStorage.setItem(storageKey, customRange)
    localStorage.setItem('cli-proxy-usage-api-key-filter-v1', '11')
    Object.values(api).forEach((mock) => mock.mockReset())
    overviewAPI.mockResolvedValue(overview('a'))
    api.fetchUsageOverviewComparisons.mockResolvedValue(comparisons('a'))
    api.fetchUsageEvents.mockResolvedValue({ events: [], total_count: 0, has_more: false })
    api.openUsageEventsLive.mockImplementation(async (range, options, signal) => requestEventsLiveSnapshot(await api.fetchUsageEvents(range, signal, options), signal))
    api.fetchUsageOverviewRealtime.mockResolvedValue({ window: '15m' })
    api.fetchAnalysis.mockResolvedValue({})
    api.fetchAnalysisLatency.mockResolvedValue({})
    for (const mock of [api.fetchUsageActivity, api.fetchKeyActivity]) mock.mockResolvedValue({ window: 'week', blocks: [], rows: 1, columns: 1, bucket_seconds: 60 })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
    useUsageStatsStore.getState().clearUsageStats()
    localStorage.clear()
  })

  it('compares only settled results, offers an explicit bounded refresh, and preserves custom range/timezone/key', async () => {
    let resolveComparisons!: (value: ReturnType<typeof comparisons>) => void
    api.fetchUsageOverviewComparisons.mockReturnValueOnce(new Promise((resolve) => { resolveComparisons = resolve }))
    await render()
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    await act(async () => resolveComparisons(comparisons('b')))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).not.toBeNull()
    expect(overviewAPI).toHaveBeenCalledTimes(1)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(1)
    const request = overviewAPI.mock.calls[0][0]
    const comparisonRequest = api.fetchUsageOverviewComparisons.mock.calls[0]
    overviewAPI.mockResolvedValue(overview('b'))
    api.fetchUsageOverviewComparisons.mockResolvedValue(comparisons('b'))
    await act(async () => container.querySelector<HTMLButtonElement>('[data-pricing-snapshot-notice] button')!.click())
    expect(overviewAPI).toHaveBeenCalledTimes(2)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(2)
    expect(overviewAPI.mock.lastCall![0]).toEqual(request)
    expect(api.fetchUsageOverviewComparisons.mock.lastCall![0]).toEqual(comparisonRequest[0])
    expect(api.fetchUsageOverviewComparisons.mock.lastCall![1]).toMatchObject({ apiKeyId: comparisonRequest[1].apiKeyId, keyViewer: viewer === 'key' })
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    expect(localStorage.getItem(storageKey)).toBe(customRange)
    expect(localStorage.getItem('cli-proxy-usage-api-key-filter-v1')).toBe('11')
    if (viewer === 'admin') {
      expect(overviewAPI.mock.lastCall![2]).toBe('11')
      expect(api.fetchUsageEvents).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ range: 'custom', unit: 'day', start: '2026-09-01', end: '2026-09-02' }), expect.any(AbortSignal), expect.objectContaining({ apiKeyId: '11' }))
      expect(api.fetchUsageEvents.mock.lastCall![2].cursor).toBeUndefined()
      expect(api.fetchUsageOverviewRealtime).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ apiKeyId: '11' }))
      expect(api.fetchAnalysis).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ range: 'custom', unit: 'day', start: '2026-09-01', end: '2026-09-02' }), expect.any(AbortSignal), '11')
    }
  })

  it('warns on an internally mixed Overview root/summary without automatically reloading', async () => {
    overviewAPI.mockResolvedValue({ ...overview('a'), summary: { pricing_snapshot_id: 'b' } })
    await render()
    expect(container.querySelector('[data-pricing-snapshot-notice]')).not.toBeNull()
    expect(overviewAPI).toHaveBeenCalledTimes(1)
  })

  if (viewer === 'admin') it('pins the related request page to query-current Overview/comparisons and explicitly refreshes all costs', async () => {
    await render()
    api.fetchUsageEvents.mockResolvedValue({ events: [], total_count: 0, has_more: false, pricing_snapshot_id: 'b' })
    await act(async () => container.querySelector<HTMLAnchorElement>('[data-dashboard-toolbar] a[href="/request-events"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).not.toBeNull()
    expect(api.fetchUsageEvents).toHaveBeenCalledTimes(1)
    overviewAPI.mockResolvedValue(overview('b'))
    api.fetchUsageOverviewComparisons.mockResolvedValue(comparisons('b'))
    await act(async () => container.querySelector<HTMLButtonElement>('[data-pricing-snapshot-notice] button')!.click())
    expect(overviewAPI).toHaveBeenCalledTimes(2)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(2)
    expect(api.fetchUsageEvents).toHaveBeenCalledTimes(2)
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    expect(localStorage.getItem(storageKey)).toBe(customRange)
    expect(localStorage.getItem('cli-proxy-usage-api-key-filter-v1')).toBe('11')
  })

  if (viewer === 'admin') it.each(['b', undefined])('pins Analysis root/breakdown %s/b to query-current Overview/comparisons and refreshes every related cost query', async (rootID) => {
    await render()
    api.fetchAnalysis.mockResolvedValue({ pricing_snapshot_id: rootID, cost_breakdown: { pricing_snapshot_id: 'b' } })
    await act(async () => container.querySelector<HTMLAnchorElement>('[data-dashboard-toolbar] a[href="/analysis"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).not.toBeNull()
    expect(api.fetchAnalysis).toHaveBeenCalledTimes(1)
    const request = api.fetchAnalysis.mock.calls[0]
    overviewAPI.mockResolvedValue(overview('b'))
    api.fetchUsageOverviewComparisons.mockResolvedValue(comparisons('b'))
    await act(async () => container.querySelector<HTMLButtonElement>('[data-pricing-snapshot-notice] button')!.click())
    expect(api.fetchAnalysis).toHaveBeenCalledTimes(2)
    expect(api.fetchAnalysis.mock.lastCall![0]).toEqual(request[0])
    expect(api.fetchAnalysis.mock.lastCall![2]).toBe('11')
    expect(overviewAPI).toHaveBeenCalledTimes(2)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(2)
    expect(api.fetchUsageEvents).toHaveBeenCalledTimes(1)
    expect(api.fetchUsageOverviewRealtime).toHaveBeenCalledTimes(1)
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    expect(localStorage.getItem(storageKey)).toBe(customRange)
    expect(localStorage.getItem('cli-proxy-usage-api-key-filter-v1')).toBe('11')
  })

  if (viewer === 'admin') it('warns on internally mixed Analysis IDs without an automatic reload', async () => {
    await render()
    api.fetchAnalysis.mockResolvedValue({ pricing_snapshot_id: 'a', cost_breakdown: { pricing_snapshot_id: 'b' } })
    await act(async () => container.querySelector<HTMLAnchorElement>('[data-dashboard-toolbar] a[href="/analysis"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).not.toBeNull()
    expect(api.fetchAnalysis).toHaveBeenCalledTimes(1)
  })

  if (viewer === 'admin') it('does not pin Analysis to previous-query Overview/comparisons or display an old Analysis notice during a range change', async () => {
    await render()
    api.fetchAnalysis.mockResolvedValueOnce({ pricing_snapshot_id: 'a', cost_breakdown: { pricing_snapshot_id: 'b' } })
    await act(async () => container.querySelector<HTMLAnchorElement>('[data-dashboard-toolbar] a[href="/analysis"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).not.toBeNull()
    const pending = Promise.withResolvers<unknown>()
    api.fetchAnalysis.mockReturnValueOnce(pending.promise)
    await act(async () => container.querySelector<HTMLButtonElement>('[data-time-range-trigger="desktop"]')!.click())
    await act(async () => document.querySelector<HTMLButtonElement>('[data-time-range-mode="yesterday"]')!.click())
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    await act(async () => pending.resolve({ pricing_snapshot_id: 'b', cost_breakdown: { pricing_snapshot_id: 'b' } }))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    expect(overviewAPI).toHaveBeenCalledTimes(1)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(1)
  })

  if (viewer === 'admin') it('ignores a canceled late mixed Analysis snapshot after changing API Key and does not compare the previous Key pin', async () => {
    await render()
    const previous = Promise.withResolvers<unknown>()
    api.fetchAnalysis.mockReturnValueOnce(previous.promise).mockResolvedValue({ pricing_snapshot_id: 'c', cost_breakdown: { pricing_snapshot_id: 'c' } })
    await act(async () => container.querySelector<HTMLAnchorElement>('[data-dashboard-toolbar] a[href="/analysis"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })))
    const signal = api.fetchAnalysis.mock.calls[0][1] as AbortSignal
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    await act(async () => container.querySelector<HTMLButtonElement>('[data-dashboard-toolbar] button[aria-label^="API Key: "]')!.click())
    await act(async () => Array.from(document.querySelectorAll<HTMLButtonElement>('[role="option"]')).find(node => node.textContent === 'Other Key')!.click())
    expect(signal.aborted).toBe(true)
    expect(api.fetchAnalysis.mock.lastCall![2]).toBe('33')
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    await act(async () => previous.resolve({ pricing_snapshot_id: 'a', cost_breakdown: { pricing_snapshot_id: 'b' } }))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    expect(overviewAPI).toHaveBeenCalledTimes(1)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(1)
  })

  it('does not compare the previous query snapshots while a new range is settling', async () => {
    await render()
    let resolveOverview!: (value: ReturnType<typeof overview>) => void
    let resolveComparisons!: (value: ReturnType<typeof comparisons>) => void
    overviewAPI.mockReturnValueOnce(new Promise((resolve) => { resolveOverview = resolve }))
    api.fetchUsageOverviewComparisons.mockReturnValueOnce(new Promise((resolve) => { resolveComparisons = resolve }))
    await act(async () => container.querySelector<HTMLButtonElement>('[data-time-range-trigger="desktop"]')!.click())
    await act(async () => document.querySelector<HTMLButtonElement>('[data-time-range-mode="yesterday"]')!.click())
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    await act(async () => resolveOverview(overview('b')))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    await act(async () => resolveComparisons(comparisons('b')))
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    expect(overviewAPI).toHaveBeenCalledTimes(2)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(2)
  })

  it.each([['a', 'a'], [undefined, 'a'], ['a', undefined], [undefined, undefined]])('does not warn or reload for compatible/legacy IDs %s and %s', async (overviewID, comparisonID) => {
    overviewAPI.mockResolvedValue(overview(overviewID))
    api.fetchUsageOverviewComparisons.mockResolvedValue(comparisons(comparisonID))
    await render()
    expect(container.querySelector('[data-pricing-snapshot-notice]')).toBeNull()
    expect(overviewAPI).toHaveBeenCalledTimes(1)
    expect(api.fetchUsageOverviewComparisons).toHaveBeenCalledTimes(1)
  })

  it.each(['overview', 'comparisons'] as const)('does not display an upstream %s error body', async (endpoint) => {
    const mock = endpoint === 'overview' ? overviewAPI : api.fetchUsageOverviewComparisons
    mock.mockRejectedValueOnce(new Error('upstream-secret-body'))
    await render()
    expect(container.textContent).not.toContain('upstream-secret-body')
    expect(container.textContent).toContain(i18n.t('cost_estimates.load_failed'))
  })
})
