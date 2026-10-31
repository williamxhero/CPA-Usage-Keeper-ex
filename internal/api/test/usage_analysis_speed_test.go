package test

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestUsageAnalysisReturnsSpeedBoundsAndNullSamples(t *testing.T) {
	start := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	end := start.Add(3 * time.Hour)
	mean := 40.0
	provider := &analysisSplitStub{analysis: &servicedto.AnalysisSnapshot{ModelSpeed: servicedto.AnalysisModelSpeed{
		RangeStart: &start, RangeEnd: &end, Buckets: []servicedto.AnalysisModelSpeedBucket{{Start: start, End: end}}, Series: []servicedto.AnalysisModelSpeedSeries{
			{Model: "a", TotalTokens: 300, SpeedSampleCount: 3, Samples: []servicedto.AnalysisModelSpeedSample{{SpeedTPSSum: 120, SpeedSampleCount: 3, MeanTPS: &mean}}},
			{Model: "b", TotalTokens: 1000, Samples: []servicedto.AnalysisModelSpeedSample{{}}},
		},
	}}}
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "")
	response := serveAPIGet(router, "/api/v1/usage/analysis?range=24h")
	if response.Code != http.StatusOK {
		t.Fatalf("status: %d", response.Code)
	}
	var payload struct {
		ModelSpeed struct {
			RangeStart time.Time                        `json:"range_start"`
			RangeEnd   time.Time                        `json:"range_end"`
			Buckets    []struct{ Start, End time.Time } `json:"buckets"`
			Series     []struct {
				Model            string `json:"model"`
				TotalTokens      int64  `json:"total_tokens"`
				SpeedSampleCount int64  `json:"speed_sample_count"`
				Samples          []struct {
					SpeedTPSSum      float64  `json:"speed_tps_sum"`
					SpeedSampleCount int64    `json:"speed_sample_count"`
					MeanTPS          *float64 `json:"mean_tps"`
				} `json:"samples"`
			} `json:"series"`
		} `json:"model_speed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	speed := payload.ModelSpeed
	if !speed.RangeStart.Equal(start) || !speed.RangeEnd.Equal(end) || len(speed.Buckets) != 1 || !speed.Buckets[0].End.Equal(end) || len(speed.Series) != 2 {
		t.Fatalf("bounds/series: %s", response.Body.String())
	}
	if speed.Series[0].Samples[0].MeanTPS == nil || math.Abs(*speed.Series[0].Samples[0].MeanTPS-40) > 1e-12 || math.Abs(speed.Series[0].Samples[0].SpeedTPSSum-120) > 1e-12 || speed.Series[0].SpeedSampleCount != 3 || speed.Series[1].Samples[0].MeanTPS != nil {
		t.Fatalf("samples: %s", response.Body.String())
	}
}

func TestUsageAnalysisEmptySpeedUsesArrays(t *testing.T) {
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "")
	response := serveAPIGet(router, "/api/v1/usage/analysis?range=24h")
	var payload struct {
		ModelSpeed struct {
			Buckets []json.RawMessage `json:"buckets"`
			Series  []json.RawMessage `json:"series"`
		} `json:"model_speed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ModelSpeed.Buckets == nil || payload.ModelSpeed.Series == nil {
		t.Fatal("expected empty arrays")
	}
}
