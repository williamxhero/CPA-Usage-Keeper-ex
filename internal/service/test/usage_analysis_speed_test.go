package test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestUsageServiceAnalysisMapsSpeedWithinKeyFilter(t *testing.T) {
	db := openUsageServiceTestDatabase(t)
	keys := []entities.CPAAPIKey{{APIKey: "speed-service-a"}, {APIKey: "speed-service-b"}}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	end := start.Add(time.Hour)
	cost, unavailable := 0.0, int64(0)
	rows := []entities.UsageOverviewHourlyStat{
		{BucketStart: start, APIGroupKey: keys[0].APIKey, Model: "a", TotalTokens: 100, SpeedTPSSum: 70, SpeedSampleCount: 2, CostUSD: &cost, UnavailableCostCount: &unavailable},
		{BucketStart: start, APIGroupKey: keys[1].APIKey, Model: "b", TotalTokens: 999, SpeedTPSSum: 999, SpeedSampleCount: 1, CostUSD: &cost, UnavailableCostCount: &unavailable},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	provider := service.NewUsageService(db, emptyPricingCatalogForTest())
	result, err := provider.GetAnalysis(context.Background(), servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", APIKeyID: strconv.FormatInt(keys[0].ID, 10), StartTime: &start, EndTime: &end, EndExclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	speed := result.ModelSpeed
	if len(speed.Buckets) != 1 || len(speed.Series) != 1 || speed.Series[0].Model != "a" || speed.Series[0].TotalTokens != 100 || speed.Series[0].SpeedSampleCount != 2 || speed.Series[0].Samples[0].MeanTPS == nil {
		t.Fatalf("speed mapping/filter: %+v", speed)
	}
	if !usageFilterCostClose(*speed.Series[0].Samples[0].MeanTPS, 35) {
		t.Fatal("wrong mean")
	}
	if _, err := provider.GetAnalysis(context.Background(), servicedto.UsageFilter{Range: "custom", APIKeyID: "99999", StartTime: &start, EndTime: &end}); err == nil {
		t.Fatal("invalid key must remain rejected")
	}
}
