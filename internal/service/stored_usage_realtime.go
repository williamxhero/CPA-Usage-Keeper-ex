package service

import (
	"context"
	"fmt"

	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// StoredUsageRealtimeProvider is deliberately not part of UsageProvider.
// Constructing it cannot change the monetary contract of existing consumers.
type StoredUsageRealtimeProvider interface {
	GetStoredUsageRealtime(context.Context, servicedto.StoredUsageRealtimeFilter) (*servicedto.StoredUsageRealtime, error)
}

type storedUsageRealtimeService struct {
	db    *gorm.DB
	cache *repository.UsageRecentEventCache
}

func NewStoredUsageRealtimeService(db *gorm.DB, cache *repository.UsageRecentEventCache) StoredUsageRealtimeProvider {
	return &storedUsageRealtimeService{db: db, cache: cache}
}

type StoredUsageRealtimeValidationError struct{}

func (*StoredUsageRealtimeValidationError) Error() string { return "invalid stored realtime filter" }

func (s *storedUsageRealtimeService) GetStoredUsageRealtime(ctx context.Context, f servicedto.StoredUsageRealtimeFilter) (*servicedto.StoredUsageRealtime, error) {
	ctx = usageServiceContext(ctx)
	if f.RealtimeWindow != "15m" && f.RealtimeWindow != "30m" && f.RealtimeWindow != "60m" || f.RealtimeEndTime != nil && f.RealtimeEndTime.IsZero() {
		return nil, &StoredUsageRealtimeValidationError{}
	}
	if s.db == nil {
		return nil, fmt.Errorf("stored realtime database is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := (&usageService{db: s.db}).resolveAPIGroupKey(ctx, f.APIKeyID)
	if err != nil {
		return nil, err
	}
	r, err := repository.BuildStoredUsageRealtimeWithFilterAndRecentCache(s.db.WithContext(ctx).Clauses(dbresolver.Read), repodto.UsageQueryFilter{
		RealtimeWindow: f.RealtimeWindow, RealtimeEndTime: f.RealtimeEndTime, APIGroupKey: key,
	}, s.cache)
	if err != nil {
		return nil, err
	}
	return &servicedto.StoredUsageRealtime{
		Window: r.Window, BucketSeconds: r.BucketSeconds, WindowStart: r.WindowStart, WindowEnd: r.WindowEnd, CacheUsed: r.CacheUsed,
		Insights: &r.Insights, TokenVelocity: r.TokenVelocity, ModelSpeed: r.ModelSpeed, LatencyScatter: r.LatencyScatter,
		CurrentUsage: r.CurrentUsage, RequestLevel: r.RequestLevel, CacheLevel: r.CacheLevel,
	}, nil
}
