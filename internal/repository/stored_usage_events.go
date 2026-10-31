package repository

import (
	"fmt"
	"math"

	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
)

const storedUsageEventProjectionColumns = usageEventProjectionColumns + ", cost_usd, cost_available"

// ListStoredUsageEventsWithFilter explicitly reads persisted fees. It reuses the
// ordinary Events filters and timestamp/id ordering, never a pricing resolver.
// Like ordinary Events it reads only usage_events: no archive union, hourly
// aggregation, backfill, pricing lookup, or watermark/configuration writes.
func ListStoredUsageEventsWithFilter(db *gorm.DB, filter dto.UsageQueryFilter) (*dto.StoredUsageEventsPageRecord, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	page, pageSize, offset, err := storedUsageEventsPagination(filter)
	if err != nil {
		return nil, err
	}
	totalCount := int64(-1)
	if !filter.SkipTotalCount && (!filter.CursorMode || filter.CursorTimestamp == nil) {
		if err := applyUsageEventListQuery(queryUsageEvents(db), filter).Count(&totalCount).Error; err != nil {
			return nil, fmt.Errorf("count stored usage events: %w", err)
		}
	}
	query := applyUsageEventListQuery(queryUsageEvents(db), filter)
	if filter.CursorMode && filter.CursorTimestamp != nil {
		at := timeutil.FormatStorageTime(*filter.CursorTimestamp)
		query = query.Where("(timestamp < ? OR (timestamp = ? AND id < ?))", at, at, filter.CursorID)
	}
	limit := pageSize
	if filter.CursorMode {
		limit++
	}
	query = query.Select(storedUsageEventProjectionColumns).Order("timestamp DESC, id DESC").Limit(limit)
	if !filter.CursorMode {
		query = query.Offset(offset)
	}
	events := []dto.StoredUsageEventRecord{}
	if err := streamStoredUsageEventsForQuery(db, query, func(row dto.StoredUsageEventRecord) error { events = append(events, row); return nil }); err != nil {
		return nil, err
	}
	hasMore := filter.CursorMode && len(events) > pageSize
	if hasMore {
		events = events[:pageSize]
	}
	totalPages := 0
	if totalCount > 0 {
		totalPages = int((totalCount-1)/int64(pageSize) + 1)
	}
	return &dto.StoredUsageEventsPageRecord{Events: events, TotalCount: totalCount, Page: page, PageSize: pageSize, TotalPages: totalPages, HasMore: hasMore, DataSource: "usage_events"}, nil
}

// StreamStoredUsageEventsWithFilter uses the same hot-table filter contract as
// ordinary export: pagination and list cursors are ignored; rows are scanned and
// emitted one at a time. Cancel through db.WithContext(ctx) or return an error
// from emit. No resolver parameter or implicit nil-resolver mode exists.
func StreamStoredUsageEventsWithFilter(db *gorm.DB, filter dto.UsageQueryFilter, emit func(dto.StoredUsageEventRecord) error) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if emit == nil {
		return fmt.Errorf("stored usage event stream callback is nil")
	}
	query := applyUsageEventListQuery(queryUsageEvents(db), filter).Select(storedUsageEventProjectionColumns).Order("timestamp DESC, id DESC")
	return streamStoredUsageEventsForQuery(db, query, emit)
}

// Keep ordinary default/fallback/offset behavior, rejecting arithmetic overflow
// rather than allowing a negative GORM Limit to turn a bounded list into a scan.
func storedUsageEventsPagination(filter dto.UsageQueryFilter) (page, pageSize, offset int, err error) {
	page = filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize = filter.PageSize
	if pageSize <= 0 {
		pageSize = filter.Limit
	}
	if pageSize <= 0 {
		pageSize = dto.DefaultUsageEventsLimit
	}
	if filter.CursorMode && pageSize == math.MaxInt {
		return 0, 0, 0, fmt.Errorf("stored usage event page size overflow")
	}
	offset = filter.Offset
	if offset <= 0 {
		if page-1 > math.MaxInt/pageSize {
			return 0, 0, 0, fmt.Errorf("stored usage event offset overflow")
		}
		offset = (page - 1) * pageSize
	}
	return
}

func streamStoredUsageEventsForQuery(db, query *gorm.DB, emit func(dto.StoredUsageEventRecord) error) error {
	ctx := query.Statement.Context
	if err := ctx.Err(); err != nil {
		return err
	}
	rows, err := query.Rows()
	if err != nil {
		return fmt.Errorf("load stored usage events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event usageEventProjection
		if err := db.ScanRows(rows, &event); err != nil {
			return fmt.Errorf("scan stored usage event: %w", err)
		}
		if event.CostUSD == nil || event.CostAvailable == nil {
			return fmt.Errorf("stored usage event %d has uninitialized persisted fee", event.ID)
		}
		if math.IsNaN(*event.CostUSD) || math.IsInf(*event.CostUSD, 0) {
			return fmt.Errorf("stored usage event %d has non-finite persisted fee", event.ID)
		}
		// The shared mapper only normalizes metadata; it never evaluates pricing.
		metadata := usageEventProjectionToRecord(event)
		record := dto.StoredUsageEventRecord{
			Metadata: dto.StoredUsageEventMetadata{
				ID: metadata.ID, Timestamp: metadata.Timestamp, APIGroupKey: metadata.APIGroupKey,
				Model: metadata.Model, ModelAlias: metadata.ModelAlias, ResponseModel: metadata.ResponseModel,
				ReasoningEffort: metadata.ReasoningEffort, ServiceTier: metadata.ServiceTier, EffectiveServiceTier: metadata.EffectiveServiceTier, ResponseServiceTier: metadata.ResponseServiceTier,
				ClientIP: metadata.ClientIP, XForwardedFor: metadata.XForwardedFor, UserAgent: metadata.UserAgent,
				ExecutorType: metadata.ExecutorType, Endpoint: metadata.Endpoint, AuthType: metadata.AuthType,
				ObservedAuthType: event.AuthType, ObservedAuthIndex: event.AuthIndex,
				RequestID: metadata.RequestID, Provider: metadata.Provider, Source: metadata.Source, AuthIndex: metadata.AuthIndex,
				Failed: metadata.Failed, StatusCode: metadata.StatusCode, Stream: metadata.Stream, LatencyMS: metadata.LatencyMS, TTFTMS: metadata.TTFTMS,
				InputTokens: metadata.InputTokens, OutputTokens: metadata.OutputTokens, ReasoningTokens: metadata.ReasoningTokens, CacheReadTokens: metadata.CacheReadTokens, CacheCreationTokens: metadata.CacheCreationTokens, TotalTokens: metadata.TotalTokens,
			},
			StoredCostUSD: *event.CostUSD, StoredCostAvailable: *event.CostAvailable,
		}
		if err := emit(record); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stored usage events: %w", err)
	}
	return ctx.Err()
}
