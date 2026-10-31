package test

import (
	"reflect"
	"time"
	"unsafe"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	. "cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"gorm.io/gorm"
)

// 以下入口保留原统计 oracle 和固定边界测试，不向生产包增加导出接口。
//
//go:linkname computeWindowMinutes cpa-usage-keeper/internal/repository.computeWindowMinutes
func computeWindowMinutes(filter dto.UsageQueryFilter) int64

//go:linkname shouldBucketUsageOverviewByDay cpa-usage-keeper/internal/repository.shouldBucketUsageOverviewByDay
func shouldBucketUsageOverviewByDay(filter dto.UsageQueryFilter, windowMinutes int64) bool

//go:linkname newUsageOverviewRecord cpa-usage-keeper/internal/repository.newUsageOverviewRecord
func newUsageOverviewRecord(windowMinutes int64) *dto.UsageOverviewRecord

//go:linkname applyUsageEventToOverviewSnapshot cpa-usage-keeper/internal/repository.applyUsageEventToOverviewSnapshot
func applyUsageEventToOverviewSnapshot(snapshot *dto.StatisticsSnapshot, event entities.UsageEvent)

// 生产辅助函数增加了可选的身份查询切片，此处保持实际私有类型的可变参数声明以匹配 ABI。
// 同包测试桥不引入新的生产 public API。
//
//go:linkname applyUsageEventToOverview cpa-usage-keeper/internal/repository.applyUsageEventToOverview
func applyUsageEventToOverview(overview *dto.UsageOverviewRecord, event entities.UsageEvent, bucketByDay bool, costResolver pricing.Resolver, identityLookups ...any)

//go:linkname finalizeUsageOverview cpa-usage-keeper/internal/repository.finalizeUsageOverview
func finalizeUsageOverview(overview *dto.UsageOverviewRecord)

//go:linkname usageOverviewBucketABI cpa-usage-keeper/internal/repository.usageOverviewBucket
func usageOverviewBucketABI(timestamp time.Time, byDay bool) (string, int64)

// Keep the historical single-value test helper local; the linkname bridge above
// preserves the production pair return without an unsafe call-site mismatch.
func usageOverviewBucket(timestamp time.Time, byDay bool) string {
	key, _ := usageOverviewBucketABI(timestamp, byDay)
	return key
}

//go:linkname usageOverviewRealtimeWindow cpa-usage-keeper/internal/repository.usageOverviewRealtimeWindow
func usageOverviewRealtimeWindow(value string) (time.Duration, time.Duration)

//go:linkname usageOverviewRealtimeWindowLabel cpa-usage-keeper/internal/repository.usageOverviewRealtimeWindowLabel
func usageOverviewRealtimeWindowLabel(window time.Duration) string

//go:linkname usageOverviewRealtimeAggregationWindow cpa-usage-keeper/internal/repository.usageOverviewRealtimeAggregationWindow
func usageOverviewRealtimeAggregationWindow(window time.Duration) time.Duration

//go:linkname usageOverviewRealtimeAggregationBucketCount cpa-usage-keeper/internal/repository.usageOverviewRealtimeAggregationBucketCount
func usageOverviewRealtimeAggregationBucketCount(span, aggregationWindow time.Duration) int

//go:linkname usageOverviewRealtimeScatterPointRange cpa-usage-keeper/internal/repository.usageOverviewRealtimeScatterPointRange
func usageOverviewRealtimeScatterPointRange(index, pointCount, maxPoints int) (int, int)

//go:linkname newEmptyUsageRecentEventCache cpa-usage-keeper/internal/repository.newEmptyUsageRecentEventCache
func newEmptyUsageRecentEventCache(opts UsageRecentEventCacheOptions) *UsageRecentEventCache

//go:linkname appendRecentCacheEvents cpa-usage-keeper/internal/repository.(*UsageRecentEventCache).appendEvents
func appendRecentCacheEvents(cache *UsageRecentEventCache, events []entities.UsageEvent)

func recentCacheField[T any](cache *UsageRecentEventCache, name string) *T {
	field := reflect.ValueOf(cache).Elem().FieldByName(name)
	return (*T)(unsafe.Pointer(field.UnsafeAddr()))
}

func hasCachedCredentialHealthKey(cache *UsageRecentEventCache, authType, authIndex string) bool {
	buckets := reflect.ValueOf(cache).Elem().FieldByName("credentialHealth").FieldByName("bucketsByCredential")
	for _, key := range buckets.MapKeys() {
		if key.FieldByName("authType").String() == authType && key.FieldByName("authIndex").String() == authIndex {
			return true
		}
	}
	return false
}

// 只传递切片头，不读写、遍历或重新分配元素，避免复制生产私有行结构的内存布局。
// 健康行仅统计批次长度，字段值经公开健康快照验证。
type opaqueCredentialHealthRows []struct{}

//go:linkname loadCredentialHealthCacheRowsBatched cpa-usage-keeper/internal/repository.loadCredentialHealthCacheRowsBatched
func loadCredentialHealthCacheRowsBatched(db *gorm.DB, start, anchor time.Time, batchSize int, handle func(opaqueCredentialHealthRows) error) error
