package dto

import (
	"time"

	repodto "cpa-usage-keeper/internal/repository/dto"
)

// StoredUsageRealtimeFilter has no current-price/catalog mode or large-range
// filters. The service validates its bounded window and active API-key scope.
type StoredUsageRealtimeFilter struct {
	RealtimeWindow  string
	RealtimeEndTime *time.Time
	APIKeyID        string
}

// StoredUsageRealtime is an internal service result, not an HTTP response.
// Public identity mapping and redaction are required at the HTTP boundary.
type StoredUsageRealtime struct {
	Window         string
	BucketSeconds  int64
	WindowStart    time.Time
	WindowEnd      time.Time
	CacheUsed      bool
	Insights       *repodto.StoredRealtimeInsightsRecord
	TokenVelocity  []repodto.StoredRealtimeTokenVelocityPointRecord
	ModelSpeed     repodto.RealtimeModelSpeedRecord
	LatencyScatter repodto.RealtimeLatencyScatterRecord
	CurrentUsage   repodto.StoredRealtimeCurrentUsageRecord `json:"-"`
	RequestLevel   []repodto.RealtimeRequestLevelPointRecord
	CacheLevel     []repodto.RealtimeCacheLevelPointRecord
}
