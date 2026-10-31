package service

import (
	"context"
	"fmt"
	"time"

	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
)

// StoredUsageAnalysisProvider is independent of the current pricing catalog.
type StoredUsageAnalysisProvider interface {
	GetStoredAnalysis(context.Context, servicedto.StoredAnalysisFilter) (*servicedto.AnalysisSnapshot, error)
}

type storedUsageAnalysisService struct{ db *gorm.DB }

func NewStoredUsageAnalysisService(db *gorm.DB) StoredUsageAnalysisProvider {
	return &storedUsageAnalysisService{db: db}
}

func (s *storedUsageAnalysisService) GetStoredAnalysis(ctx context.Context, f servicedto.StoredAnalysisFilter) (*servicedto.AnalysisSnapshot, error) {
	ctx = usageServiceContext(ctx)
	if f.StartTime == nil || f.EndTime == nil || f.StartTime.IsZero() || f.EndTime.IsZero() || !f.StartTime.Before(*f.EndTime) {
		return nil, fmt.Errorf("invalid stored analysis filter")
	}
	if f.Range == "custom" && f.CustomUnit != "hour" && f.CustomUnit != "day" {
		return nil, fmt.Errorf("invalid stored analysis filter")
	}
	if f.Range != "custom" && f.Range != "today" && f.Range != "yesterday" {
		if _, ok := timeutil.ParseUsageRollingRange(f.Range); !ok {
			return nil, fmt.Errorf("invalid stored analysis filter")
		}
	}
	if f.Range == "custom" {
		span := f.EndTime.Sub(*f.StartTime)
		if (f.CustomUnit == "hour" && span > 24*time.Hour) || (f.CustomUnit == "day" && timeutil.DateOf(*f.StartTime).DaysUntil(timeutil.DateOf(*f.EndTime)) > timeutil.LongCustomDayRangeMaxDays) {
			return nil, fmt.Errorf("invalid stored analysis filter")
		}
	}
	if s.db == nil {
		return nil, fmt.Errorf("stored analysis database is nil")
	}
	key, err := (&usageService{db: s.db}).resolveAPIGroupKey(ctx, f.APIKeyID)
	if err != nil {
		return nil, err
	}
	record, err := repository.BuildStoredAnalysisWithFilter(s.db.WithContext(ctx), repodto.UsageQueryFilter{
		Range: f.Range, CustomUnit: f.CustomUnit, StartTime: f.StartTime, EndTime: f.EndTime, EndExclusive: f.EndExclusive, QueryNow: f.QueryNow, APIGroupKey: key,
	})
	if err != nil {
		return nil, err
	}
	return mapAnalysisRecord(record), nil
}
