package test

import (
	"testing"

	"cpa-usage-keeper/internal/entities"
)

func TestUpstreamRankingRetirementPreservesForkData(t *testing.T) {
	db := openUnmigratedTestDatabase(t)
	if err := db.AutoMigrate(&entities.LocalRankingPeriodStat{}, &entities.CPAAPIKey{}, &entities.AppSetting{}); err != nil {
		t.Fatalf("create ranking schema: %v", err)
	}
	if err := db.Exec("INSERT INTO local_ranking_period_stats (period_kind, period_key, api_key_id, request_count, updated_at) VALUES ('day', '2026-10-29', 7, 11, '2026-10-29')").Error; err != nil {
		t.Fatalf("seed ranking stat: %v", err)
	}
	if err := db.Exec("INSERT INTO cpa_api_keys (id, api_key, local_ranking_avatar_id) VALUES (7, 'synthetic-key', 3)").Error; err != nil {
		t.Fatalf("seed ranking avatar: %v", err)
	}
	if err := db.Exec("INSERT INTO app_settings (setting_key, value, value_type, created_at, updated_at) VALUES ('ranking.identity', 'synthetic', 'json', '2026-10-29', '2026-10-29')").Error; err != nil {
		t.Fatalf("seed ranking setting: %v", err)
	}

	runOnlyMigration(t, db, "20261009_remove_ranking")

	var count int64
	if err := db.Table("local_ranking_period_stats").Where("api_key_id = ? AND request_count = ?", 7, 11).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("ranking period stat lost: count=%d err=%v", count, err)
	}
	if !db.Migrator().HasColumn("cpa_api_keys", "local_ranking_avatar_id") {
		t.Fatal("ranking avatar column removed")
	}
	var avatar int
	if err := db.Raw("SELECT local_ranking_avatar_id FROM cpa_api_keys WHERE id = ?", 7).Scan(&avatar).Error; err != nil || avatar != 3 {
		t.Fatalf("ranking avatar lost: value=%d err=%v", avatar, err)
	}
	if err := db.Table("app_settings").Where("setting_key = ?", "ranking.identity").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("ranking identity lost: count=%d err=%v", count, err)
	}
}
