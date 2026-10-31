package dto

import (
	"time"

	"cpa-usage-keeper/internal/pricing"
)

type AnalysisGranularity string

const (
	AnalysisGranularityHourly AnalysisGranularity = "hourly"
	AnalysisGranularityDaily  AnalysisGranularity = "daily"
)

type AnalysisTokenUsageBucket struct {
	DualCosts           pricing.DualCosts
	Bucket              time.Time
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	TotalTokens         int64
	Requests            int64
	CostUSD             float64
	CostAvailable       bool
}

type AnalysisModelUsage struct {
	Bucket      time.Time
	Model       string
	TotalTokens int64
	Requests    int64
}

// AnalysisModelSpeed 保存跨源桶的 sum/count，显示桶在仓储层使用真实时间边界合并。
type AnalysisModelSpeed struct {
	RangeStart *time.Time
	RangeEnd   *time.Time
	Buckets    []AnalysisModelSpeedBucket
	Series     []AnalysisModelSpeedSeries
}

type AnalysisModelSpeedBucket struct {
	Start time.Time
	End   time.Time
}

type AnalysisModelSpeedSample struct {
	SpeedTPSSum      float64
	SpeedSampleCount int64
	MeanTPS          *float64
}

type AnalysisModelSpeedSeries struct {
	Model            string
	TotalTokens      int64
	SpeedSampleCount int64
	Samples          []AnalysisModelSpeedSample
}

type AnalysisCompositionItem struct {
	DualCosts           pricing.DualCosts
	Key                 string
	Label               string
	TotalTokens         int64
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	CostUSD             float64
	CostAvailable       bool
}

type AnalysisHeatmapCell struct {
	DualCosts           pricing.DualCosts
	APIKey              string
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	TotalTokens         int64
	Requests            int64
	CostUSD             float64
	CostAvailable       bool
}

type AnalysisCostBreakdown struct {
	DualCosts            pricing.DualCosts
	UnavailableReason    string
	PricingSnapshotID    string
	UncachedInputCostUSD float64
	CacheReadCostUSD     float64
	CacheWriteCostUSD    float64
	OutputCostUSD        float64
	TotalCostUSD         float64
	CostAvailable        bool
}

// AnalysisCostSummary is separate from the fork's current-snapshot cost breakdown.
type AnalysisCostSummary struct {
	TotalCostUSD  float64
	CostAvailable bool
}

type AnalysisModelEfficiencyItem struct {
	DualCosts              pricing.DualCosts
	Model                  string
	Requests               int64
	InputTokens            int64
	OutputTokens           int64
	CacheReadTokens        int64
	CacheCreationTokens    int64
	ReasoningTokens        int64
	TotalTokens            int64
	CostUSD                float64
	CostAvailable          bool
	CostPerRequestUSD      float64
	OutputTokensPerRequest float64
	CacheReadRate          float64
}

type AnalysisLatencyPoint struct {
	TTFTMS    int64
	LatencyMS int64
}

type AnalysisLatencyDensityCell struct {
	TTFTMinMS    int64
	TTFTMaxMS    int64
	LatencyMinMS int64
	LatencyMaxMS int64
	Count        int64
	Intensity    float64
}

type AnalysisLatencyDiagnostics struct {
	Points       []AnalysisLatencyPoint
	Density      []AnalysisLatencyDensityCell
	TotalPoints  int64
	Sampled      bool
	P95TTFTMS    int64
	P95LatencyMS int64
	MaxTTFTMS    int64
	MaxLatencyMS int64
}

// StoredAnalysisFilter is the explicit persisted-valuation analysis contract.
// It intentionally has no pricing resolver, model-price, or realtime fields.
type StoredAnalysisFilter struct {
	Range        string
	CustomUnit   string
	StartTime    *time.Time
	EndTime      *time.Time
	EndExclusive bool
	APIKeyID     string
	QueryNow     *time.Time
}

type AnalysisSnapshot struct {
	PricingSnapshotID      string
	Granularity            AnalysisGranularity
	RangeStart             *time.Time
	RangeEnd               *time.Time
	TokenUsage             []AnalysisTokenUsageBucket
	ModelSpeed             AnalysisModelSpeed
	ModelUsage             []AnalysisModelUsage
	APIKeyComposition      []AnalysisCompositionItem
	ModelComposition       []AnalysisCompositionItem
	AuthFilesComposition   []AnalysisCompositionItem
	CredentialsComposition []AnalysisCompositionItem
	AIProviderComposition  []AnalysisCompositionItem
	Heatmap                []AnalysisHeatmapCell
	CostBreakdown          AnalysisCostBreakdown
	CostSummary            AnalysisCostSummary
	ModelEfficiency        []AnalysisModelEfficiencyItem
}
