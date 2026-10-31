package test

import (
	"context"
	"testing"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestDiagnosticCurrentPricePath(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	if _, err := f.defaults.SetCredentialDefault(context.Background(), f.subjectID, "0.2"); err != nil {
		t.Fatal(err)
	}
	resolver := f.catalog.NewResolver()
	t.Logf("snapshot=%s models=%+v active_fields=%+v legacy_fields=%+v", resolver.SnapshotID(), f.catalog.Snapshot().ModelConfigs(), resolver.ActiveFields(), resolver.LegacyActiveFields())
	var checkpoints []map[string]any
	if err := f.db.Table("usage_aggregation_checkpoints").Find(&checkpoints).Error; err == nil {
		t.Logf("committed-checkpoints=%+v", checkpoints)
	}
	for i, e := range f.events {
		r := resolver.Calculate(repository.UsageEventCostSubject(e))
		t.Logf("event[%d] key=%s auth=%s model=%s alias=%s tokens=%+v result=%+v dual=%+v", i, e.EventKey, e.AuthIndex, e.Model, *e.ModelAlias, repository.UsageEventCostSubject(e).Tokens, r, r.DualCosts())
	}
	var hourly []entities.UsageOverviewHourlyStat
	if err := f.db.Order("bucket_start").Find(&hourly).Error; err != nil {
		t.Fatal(err)
	}
	for i, row := range hourly {
		t.Logf("hourly[%d]=%+v", i, row)
	}
	var daily []entities.UsageOverviewDailyStat
	if err := f.db.Order("bucket_start").Find(&daily).Error; err != nil {
		t.Fatal(err)
	}
	for i, row := range daily {
		t.Logf("daily[%d]=%+v", i, row)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	overview, err := usage.GetUsageOverview(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("overview summary=%+v usage=%+v series=%+v", overview.Summary, overview.Usage, overview.Series)
}
