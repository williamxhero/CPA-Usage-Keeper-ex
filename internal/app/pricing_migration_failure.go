package app

import (
	"context"
	"encoding/json"

	"cpa-usage-keeper/internal/backup"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"github.com/sirupsen/logrus"
	"gorm.io/plugin/dbresolver"
)

// logLegacyPricingRollback 只在本次旧库迁移已验证备份后提示回退；提示不改变迁移状态或原始错误。
func (a *App) logLegacyPricingRollback(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	var state entities.PricingMigrationState
	if err := a.DB.Clauses(dbresolver.Write).WithContext(ctx).Where("id = ?", 1).Take(&state).Error; err != nil ||
		state.InitKind != repository.PricingInitKindLegacy || state.DataComplete || state.BackupPath == nil || state.BaselineJSON == nil {
		return
	}
	var baseline repository.PricingLegacyBaseline
	if err := json.Unmarshal([]byte(*state.BaselineJSON), &baseline); err != nil || baseline.BackupSHA256 == "" {
		return
	}
	// 失败时再次确认文件仍与已验证备份一致，避免指向缺失或已被替换的备份。
	fingerprint, err := backup.PricingBackupSHA256(ctx, *state.BackupPath)
	if err != nil || fingerprint != baseline.BackupSHA256 {
		return
	}
	logrus.WithField("backup_path", *state.BackupPath).Warn("The v1-to-v2 database migration failed. To roll back, stop Keeper v2, preserve the failed database, copy the pre-upgrade backup at backup_path to a separate WORK_DIR as app.db, and start Keeper v1 with that WORK_DIR. With the default Docker setup, mount that directory at /data so the restored file is /data/app.db. Do not open the partially migrated database with v1. Recovery returns data to the backup point; usage received after that point will not be included.")
}
