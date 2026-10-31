package dto

import repodto "cpa-usage-keeper/internal/repository/dto"

// Metadata is internal display-resolution input, never a wire payload.
type StoredUsageEventRecord struct {
	Metadata            repodto.StoredUsageEventMetadata `json:"-"`
	StoredCostUSD       float64                          `json:"stored_cost_usd"`
	StoredCostAvailable bool                             `json:"stored_cost_available"`
}
type StoredUsageEventsPage struct {
	Events                     []StoredUsageEventRecord
	TotalCount                 int64
	Page, PageSize, TotalPages int
	HasMore                    bool
	DataSource                 string
}
