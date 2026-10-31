package test

import (
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"math"
	"strings"
	"testing"
	"time"
)

func TestStoredComparisonFeePairsAndRetainedRollups(t *testing.T) {
	for _, source := range []string{"raw", "hourly", "daily"} {
		for _, tc := range []struct {
			name      string
			fee       *float64
			available *bool
			bad       bool
		}{
			{"null_amount", nil, windowAvailablePtr(true), true}, {"null_availability", windowCostPtr(0), nil, true}, {"infinity", windowCostPtr(math.Inf(1)), windowAvailablePtr(true), true}, {"nan", windowCostPtr(math.NaN()), windowAvailablePtr(true), true}, {"free", windowCostPtr(0), windowAvailablePtr(true), false}, {"unavailable_numeric", windowCostPtr(7.25), windowAvailablePtr(false), false}, {"unavailable_zero", windowCostPtr(0), windowAvailablePtr(false), false},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				withRepositoryTestLocation(t, "UTC")
				db := openTestDatabase(t)
				start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
				end := start.Add(time.Hour)
				f := dto.UsageQueryFilter{Range: "custom", CustomUnit: "hour", StartTime: &start, EndTime: &end, EndExclusive: true}
				var unavailable *int64
				if tc.available != nil {
					v := int64(0)
					if !*tc.available {
						v = 1
					}
					unavailable = &v
				}
				var err error
				switch source {
				case "raw":
					f.CustomUnit = ""
					end = start.Add(15 * time.Minute)
					err = db.Create(&entities.UsageEvent{Timestamp: start, Model: "m", CostUSD: tc.fee, CostAvailable: tc.available, TotalTokens: 10}).Error
				case "hourly":
					err = db.Create(&entities.UsageOverviewHourlyStat{BucketStart: start, Model: "m", RequestCount: 1, TotalTokens: 10, CostUSD: tc.fee, UnavailableCostCount: unavailable}).Error
				case "daily":
					f.CustomUnit = "day"
					end = start.AddDate(0, 0, 1)
					err = db.Create(&entities.UsageOverviewDailyStat{BucketStart: start, Model: "m", RequestCount: 1, TotalTokens: 10, CostUSD: tc.fee, UnavailableCostCount: unavailable}).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				if source != "raw" {
					if err := db.Migrator().DropTable(&entities.UsageEvent{}); err != nil {
						t.Fatal(err)
					}
				}
				queries := storedOverviewRejectSideEffects(t, db)
				r, _ := repository.NewStoredUsageComparisonReader(db)
				out, err := r.Read(f)
				if tc.bad {
					if err == nil || out != nil {
						t.Fatalf("unknown fee accepted %+v %v", out, err)
					}
					return
				}
				if err != nil || out.Models["m"].StoredCostUSD != *tc.fee || out.Models["m"].StoredCostAvailable != *tc.available || out.Models["m"].TotalTokens != 10 {
					t.Fatalf("fee pair %+v %v", out, err)
				}
				if source != "raw" {
					if len(*queries) != 4 {
						t.Fatalf("expected 4 bounded rollup projections %v", *queries)
					}
					for _, q := range *queries {
						if strings.Contains(q, "usage_events") {
							t.Fatal(q)
						}
					}
				}
			})
		}
	}
}

func TestStoredComparisonFractionalDisjointDSTEdges(t *testing.T) {
	for _, zone := range []string{"UTC", "Europe/Berlin", "Asia/Kathmandu", "America/Santiago"} {
		t.Run(zone, func(t *testing.T) {
			withRepositoryTestLocation(t, zone)
			db := openTestDatabase(t)
			start := time.Date(2026, 10, 25, 0, 30, 0, 125000000, time.UTC).In(time.Local)
			if zone == "America/Santiago" {
				start = time.Date(2026, 9, 6, 3, 30, 0, 125000000, time.UTC).In(time.Local)
			}
			end := start.Add(2 * time.Hour)
			full, stop := start.Truncate(time.Hour).Add(time.Hour), end.Truncate(time.Hour)
			zero := int64(0)
			yes := true
			for _, at := range []time.Time{start, start.Add(-time.Nanosecond), end.Add(-time.Nanosecond), end, full.Add(time.Minute)} {
				if err := db.Create(&entities.UsageEvent{Timestamp: at, Model: "m", CostUSD: windowCostPtr(2), CostAvailable: &yes, TotalTokens: 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for at := full; at.Before(stop); at = at.Add(time.Hour) {
				if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, Model: "m", RequestCount: 1, TotalTokens: 5, CostUSD: windowCostPtr(3), UnavailableCostCount: &zero}).Error; err != nil {
					t.Fatal(err)
				}
			}
			queries := storedOverviewRejectSideEffects(t, db)
			r, _ := repository.NewStoredUsageComparisonReader(db)
			out, err := r.Read(dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, QueryNow: &end})
			if err != nil || out.Models["m"].StoredCostUSD != 7 || out.Models["m"].Requests != 3 || out.Models["m"].TotalTokens != 7 {
				t.Fatalf("overlap or lost edges %+v %v", out, err)
			}
			for _, q := range *queries {
				if strings.Contains(q, "from `usage_events`") && (!strings.Contains(q, "timestamp >=") || !strings.Contains(q, "timestamp <")) {
					t.Fatal(q)
				}
			}
			if zone == "UTC" && len(*queries) != 6 {
				t.Fatalf("expected 4 rollup and 2 raw %v", *queries)
			}
		})
	}
}

func TestStoredComparisonLongDisjointSources(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	start := day.Add(30 * time.Minute)
	end := day.AddDate(0, 0, 8).Add(90 * time.Minute)
	zero := int64(0)
	yes := true
	for i := 1; i < 8; i++ {
		at := day.AddDate(0, 0, i)
		if err := db.Create(&entities.UsageOverviewDailyStat{BucketStart: at, Model: "m", RequestCount: 1, TotalTokens: 10, CostUSD: windowCostPtr(10), UnavailableCostCount: &zero}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, Model: "m", CostUSD: windowCostPtr(999), UnavailableCostCount: &zero}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{day.Add(time.Hour), day.AddDate(0, 0, 8)} {
		if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, Model: "m", RequestCount: 1, TotalTokens: 2, CostUSD: windowCostPtr(2), UnavailableCostCount: &zero}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{start, end.Add(-time.Second)} {
		if err := db.Create(&entities.UsageEvent{Timestamp: at, Model: "m", CostUSD: windowCostPtr(3), CostAvailable: &yes, TotalTokens: 3}).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := storedOverviewRejectSideEffects(t, db)
	r, _ := repository.NewStoredUsageComparisonReader(db)
	out, err := r.Read(dto.UsageQueryFilter{Range: "7d", StartTime: &start, EndTime: &end, QueryNow: &end})
	if err != nil || out.Models["m"].StoredCostUSD != 80 || out.Models["m"].Requests != 11 || out.Models["m"].TotalTokens != 80 {
		t.Fatalf("mixed sources %+v %v", out, err)
	}
	if len(*queries) != 14 {
		t.Fatalf("expected daily+2hourly four projections each and two raw %v", *queries)
	}
}
