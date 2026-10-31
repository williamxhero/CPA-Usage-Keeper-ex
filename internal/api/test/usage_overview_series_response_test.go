package test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestLongCustomDayOverviewCapsAlignedSeriesAtNinetyPoints(t *testing.T) {
	db := openAPITestDatabase(t)

	now := time.Now().In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	start := today.AddDate(0, 0, -120)
	rows := make([]entities.UsageOverviewDailyStat, 0, 121)
	storedCost, unavailable := 0.125, int64(0)
	for bucket := start; !bucket.After(today); bucket = bucket.AddDate(0, 0, 1) {
		rows = append(rows, entities.UsageOverviewDailyStat{
			BucketStart: bucket, APIGroupKey: "provider-a", Model: "model-a",
			RequestCount: 1, SuccessCount: 1, InputTokens: 10, CacheReadTokens: 4, TotalTokens: 10,
			CostUSD: &storedCost, UnavailableCostCount: &unavailable,
		})
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed daily rollups: %v", err)
	}

	query := url.Values{
		"range": {"custom"}, "unit": {"day"},
		"start": {start.Format(time.DateOnly)}, "end": {today.Format(time.DateOnly)},
	}
	router, admin, _ := overviewHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	response := serveAPIGet(router, "/api/v1/usage/stored/overview?"+query.Encode(), admin)
	if response.Code != http.StatusOK {
		t.Fatalf("Overview status=%d body=%s", response.Code, response.Body.String())
	}

	var payload servicedto.StoredUsageOverview
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode long Overview response: %v body=%s", err, response.Body.String())
	}
	if payload.Summary.RequestCount != 121 || payload.Summary.TokenCount != 1210 {
		t.Fatalf("top totals must use the complete range, got %+v", payload.Summary)
	}
	if !overviewAPICostClose(payload.Summary.StoredCostUSD, 15.125) || !payload.Summary.StoredCostAvailable {
		t.Fatalf("summary must use stored daily fees: %+v", payload.Summary)
	}
	if len(payload.Series.Buckets) != 90 {
		t.Fatalf("expected exactly 90 merged points, got %d", len(payload.Series.Buckets))
	}
	for name, length := range map[string]int{
		"requests": len(payload.Series.Requests), "tokens": len(payload.Series.Tokens),
		"rpm": len(payload.Series.RPM), "tpm": len(payload.Series.TPM),
		"cost": len(payload.Series.StoredCostUSD), "cache_read_rate": len(payload.Series.CacheReadRate),
	} {
		if length != len(payload.Series.Buckets) {
			t.Fatalf("%s length=%d, buckets=%d", name, length, len(payload.Series.Buckets))
		}
	}
	var seriesRequests int64
	var seriesCost float64
	for _, requests := range payload.Series.Requests {
		seriesRequests += requests
	}
	for _, cost := range payload.Series.StoredCostUSD {
		seriesCost += cost
	}
	if seriesRequests != 121 {
		t.Fatalf("merged series lost requests: %d", seriesRequests)
	}
	if !overviewAPICostClose(seriesCost, 15.125) {
		t.Fatalf("merged series lost persisted fee: %g", seriesCost)
	}
	for index, bucket := range payload.Series.Buckets {
		rate := payload.Series.CacheReadRate[bucket]
		if rate == nil || *rate != 40 {
			t.Fatalf("merged cache rate[%d] = %v, want 40", index, rate)
		}
	}
}

// overviewAPICostClose 只比较有限金额，使用固定绝对及相对容差容纳正常 float64 求和尾差。
func overviewAPICostClose(got, want float64) bool {
	if math.IsNaN(got) || math.IsInf(got, 0) || math.IsNaN(want) || math.IsInf(want, 0) {
		return false
	}
	return math.Abs(got-want) <= 1e-9+1e-12*math.Max(math.Abs(got), math.Abs(want))
}
