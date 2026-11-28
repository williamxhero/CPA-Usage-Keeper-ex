package migration

import (
	"cpa-usage-keeper/internal/entities"
	"gorm.io/gorm"
)

func addErrorEventUpstreamCodeMigration(db *gorm.DB) error {
	// Fresh installs may already have this column from the current model.
	if db.Migrator().HasColumn(&entities.ErrorEvent{}, "UpstreamCode") {
		return nil
	}
	return db.Migrator().AddColumn(&entities.ErrorEvent{}, "UpstreamCode")
}
