package repository

import (
	"sort"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository/dto"
)

type realtimeModelSpeedAccumulator struct {
	record dto.RealtimeModelSpeedRecord
	models map[string]*dto.RealtimeModelSpeedSeriesRecord
}

func newRealtimeModelSpeed(start, end time.Time, available bool) *realtimeModelSpeedAccumulator {
	return &realtimeModelSpeedAccumulator{record: dto.RealtimeModelSpeedRecord{Available: available, Start: start, Mid: start.Add(end.Sub(start) / 2), End: end, Series: []dto.RealtimeModelSpeedSeriesRecord{}}, models: map[string]*dto.RealtimeModelSpeedSeriesRecord{}}
}

// add 在同一次可见缓存事件遍历中累计速度；失败、Generate 和总 Token 不改变速度有效条件。
func (a *realtimeModelSpeedAccumulator) add(event entities.UsageEvent, timestamp time.Time) {
	model := normalizeUsageOverviewDimension(event.Model)
	series := a.models[model]
	if series == nil {
		series = &dto.RealtimeModelSpeedSeriesRecord{Model: model}
		a.models[model] = series
	}
	if event.OutputTokens <= 0 || event.LatencyMS <= 0 {
		return
	}
	sample := &series.FirstHalf
	if !timestamp.Before(a.record.Mid) {
		sample = &series.SecondHalf
	}
	sample.SpeedTPSSum += float64(event.OutputTokens) * 1000 / float64(event.LatencyMS)
	sample.SpeedSampleCount++
	series.SpeedSampleCount++
}

func (a *realtimeModelSpeedAccumulator) finalize(tokenTotals map[string]*usageOverviewRealtimeTopAccumulator) dto.RealtimeModelSpeedRecord {
	for model, series := range a.models {
		// 排序复用尚未截成 Top5/Other 的原成功请求 Token 口径，缺速度样本模型也保留。
		if total := tokenTotals[model]; total != nil {
			series.TotalTokens = total.tokens
		}
		for _, sample := range []*dto.RealtimeModelSpeedSampleRecord{&series.FirstHalf, &series.SecondHalf} {
			if sample.SpeedSampleCount > 0 {
				mean := sample.SpeedTPSSum / float64(sample.SpeedSampleCount)
				sample.MeanTPS = &mean
			}
		}
		a.record.Series = append(a.record.Series, *series)
	}
	sort.Slice(a.record.Series, func(i, j int) bool {
		left, right := a.record.Series[i], a.record.Series[j]
		if left.TotalTokens == right.TotalTokens {
			return left.Model < right.Model
		}
		return left.TotalTokens > right.TotalTokens
	})
	return a.record
}
