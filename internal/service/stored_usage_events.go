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

// StoredUsageEventsProvider deliberately has no catalog/resolver dependency and
// cannot be substituted for UsageProvider's current monetary contract.
type StoredUsageEventsProvider interface {
	ListStoredUsageEvents(context.Context, servicedto.UsageFilter) (*servicedto.StoredUsageEventsPage, error)
	StreamStoredUsageEvents(context.Context, servicedto.UsageFilter, func(servicedto.StoredUsageEventRecord) error) error
}
type storedUsageEventsService struct{ db *gorm.DB }

func NewStoredUsageEventsService(db *gorm.DB) StoredUsageEventsProvider {
	return &storedUsageEventsService{db: db}
}

func (s *storedUsageEventsService) queryFilter(ctx context.Context, f servicedto.UsageFilter) (repodto.UsageQueryFilter, error) {
	if s.db == nil {
		return repodto.UsageQueryFilter{}, fmt.Errorf("database is nil")
	}
	// Reuse the existing active-Key scope validation, without a pricing service.
	key, err := (&usageService{db: s.db}).resolveAPIGroupKey(ctx, f.APIKeyID)
	if err != nil {
		return repodto.UsageQueryFilter{}, err
	}
	return repodto.UsageQueryFilter{Range: f.Range, CustomUnit: f.CustomUnit, StartTime: f.StartTime, EndTime: f.EndTime, EndExclusive: f.EndExclusive, Limit: f.Limit, Page: f.Page, PageSize: f.PageSize, Offset: f.Offset, CursorMode: f.CursorMode, CursorTimestamp: f.CursorTimestamp, CursorID: f.CursorID, SkipTotalCount: f.SkipTotalCount, Model: f.Model, AuthIndex: f.AuthIndex, AuthType: f.AuthType, APIGroupKey: key, Result: f.Result}, nil
}
func storedUsageEvent(row repodto.StoredUsageEventRecord) servicedto.StoredUsageEventRecord {
	return servicedto.StoredUsageEventRecord{Metadata: row.Metadata, StoredCostUSD: row.StoredCostUSD, StoredCostAvailable: row.StoredCostAvailable}
}
func (s *storedUsageEventsService) ListStoredUsageEvents(ctx context.Context, f servicedto.UsageFilter) (*servicedto.StoredUsageEventsPage, error) {
	ctx = usageServiceContext(ctx)
	filter, err := s.queryFilter(ctx, f)
	if err != nil {
		return nil, err
	}
	var page *repodto.StoredUsageEventsPageRecord
	// Count and bounded page belong to one reader snapshot, not a writer tx.
	err = s.db.WithContext(ctx).Clauses(dbresolver.Read).Transaction(func(tx *gorm.DB) error {
		var e error
		page, e = repository.ListStoredUsageEventsWithFilter(tx, filter)
		return e
	})
	if err != nil {
		return nil, err
	}
	result := &servicedto.StoredUsageEventsPage{Events: make([]servicedto.StoredUsageEventRecord, 0, len(page.Events)), TotalCount: page.TotalCount, Page: page.Page, PageSize: page.PageSize, TotalPages: page.TotalPages, HasMore: page.HasMore, DataSource: page.DataSource}
	for _, row := range page.Events {
		result.Events = append(result.Events, storedUsageEvent(row))
	}
	return result, nil
}
func (s *storedUsageEventsService) StreamStoredUsageEvents(ctx context.Context, f servicedto.UsageFilter, emit func(servicedto.StoredUsageEventRecord) error) error {
	ctx = usageServiceContext(ctx)
	if emit == nil {
		return fmt.Errorf("stored usage event callback is nil")
	}
	filter, err := s.queryFilter(ctx, f)
	if err != nil {
		return err
	}
	// A single SELECT holds the SQLite read snapshot until Rows.Close. No second
	// validity scan, TOCTOU window, long explicit transaction, or all-row buffer.
	return repository.StreamStoredUsageEventsWithFilter(s.db.WithContext(ctx).Clauses(dbresolver.Read), filter, func(row repodto.StoredUsageEventRecord) error { return emit(storedUsageEvent(row)) })
}
