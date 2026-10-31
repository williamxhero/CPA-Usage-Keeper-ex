package service

import (
	"context"
	"fmt"
	"time"

	"cpa-usage-keeper/internal/timeutil"

	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"gorm.io/gorm"
)

// StoredUsageOverviewProvider is independent of UsageProvider and current pricing.
type StoredUsageOverviewProvider interface {
	GetStoredUsageOverview(context.Context, servicedto.StoredUsageOverviewFilter) (*servicedto.StoredUsageOverview, error)
}
type storedUsageOverviewService struct {
	db          *gorm.DB
	recentCache *repository.UsageRecentEventCache
}

func NewStoredUsageOverviewService(db *gorm.DB) StoredUsageOverviewProvider {
	return &storedUsageOverviewService{db: db}
}

// NewStoredUsageOverviewServiceWithRecentCache explicitly enables covered hot
// edges from the committed-event cache. The default HTTP service stays uncached.
func NewStoredUsageOverviewServiceWithRecentCache(db *gorm.DB, cache *repository.UsageRecentEventCache) StoredUsageOverviewProvider {
	return &storedUsageOverviewService{db: db, recentCache: cache}
}

// StoredUsageOverviewValidationError is safe to classify, not to expose verbatim.
// SQL/database errors never implement this type and must remain non-success.
type StoredUsageOverviewValidationError struct{}

func (*StoredUsageOverviewValidationError) Error() string { return "invalid stored overview filter" }

func (s *storedUsageOverviewService) GetStoredUsageOverview(ctx context.Context, f servicedto.StoredUsageOverviewFilter) (*servicedto.StoredUsageOverview, error) {
	ctx = usageServiceContext(ctx)
	if f.StartTime == nil || f.EndTime == nil || f.StartTime.IsZero() || f.EndTime.IsZero() || !f.StartTime.Before(*f.EndTime) || (f.Range == "custom" && f.CustomUnit != "" && f.CustomUnit != "hour" && f.CustomUnit != "day") {
		return nil, &StoredUsageOverviewValidationError{}
	}
	if f.Range != "custom" && f.Range != "today" && f.Range != "yesterday" {
		if _, ok := timeutil.ParseUsageRollingRange(f.Range); !ok {
			return nil, &StoredUsageOverviewValidationError{}
		}
	}
	if f.Range == "custom" {
		span := f.EndTime.Sub(*f.StartTime)
		if ((f.CustomUnit == "" || f.CustomUnit == "hour") && span > 24*time.Hour) || (f.CustomUnit == "day" && timeutil.DateOf(*f.StartTime).DaysUntil(timeutil.DateOf(*f.EndTime)) > timeutil.LongCustomDayRangeMaxDays) {
			return nil, &StoredUsageOverviewValidationError{}
		}
	}
	if s.db == nil {
		return nil, fmt.Errorf("stored overview database is nil")
	}
	// The existing scope lookup has no catalog dependency. No resolver mode switch.
	key, err := (&usageService{db: s.db}).resolveAPIGroupKey(ctx, f.APIKeyID)
	if err != nil {
		return nil, err
	}
	reader, err := repository.NewStoredUsageOverviewReaderWithRecentCache(s.db.WithContext(ctx), s.recentCache)
	if err != nil {
		return nil, err
	}
	record, err := reader.Read(repodto.UsageQueryFilter{Range: f.Range, CustomUnit: f.CustomUnit, StartTime: f.StartTime, EndTime: f.EndTime, EndExclusive: f.EndExclusive, QueryNow: f.QueryNow, APIGroupKey: key})
	if err != nil {
		return nil, err
	}
	a, b := record.Summary, record.Series
	series, err := mapStoredUsageOverviewSeries(b, f)
	if err != nil {
		return nil, err
	}
	// The repository reads half-open raw boundaries and complete rollup buckets.
	// Its effective end (including today's now clamp) is reported, not the input end.
	return &servicedto.StoredUsageOverview{
		Range:    servicedto.StoredUsageOverviewRange{Start: record.RangeStart, End: record.RangeEnd, EndExclusive: true, Timezone: time.Local.String()},
		Coverage: servicedto.StoredUsageOverviewCoverage{BoundarySource: record.BoundarySource, RollupSources: []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"}, ArchiveCoverageAsserted: false, CacheUsed: record.CacheUsed},
		Summary:  servicedto.StoredUsageOverviewSummary{RequestCount: a.RequestCount, TokenCount: a.TokenCount, WindowMinutes: a.WindowMinutes, RPM: a.RPM, TPM: a.TPM, StoredCostUSD: a.StoredCostUSD, StoredCostAvailable: a.StoredCostAvailable, InputTokens: a.InputTokens, CacheReadTokens: a.CacheReadTokens, CacheCreationTokens: a.CacheCreationTokens, ReasoningTokens: a.ReasoningTokens, DailyAverageRequests: a.DailyAverageRequests, DailyAverageTokens: a.DailyAverageTokens, DailyAverageStoredCostUSD: a.DailyAverageStoredCostUSD, DailyAverageRangeDays: a.DailyAverageRangeDays},
		Series:   series,
	}, nil
}
