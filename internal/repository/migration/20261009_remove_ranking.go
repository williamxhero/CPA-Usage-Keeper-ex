package migration

import "gorm.io/gorm"

// The fork retains local ranking, including its period stats, avatar setting and
// identity preferences. Mark the upstream retirement version without deleting
// that data; existing deployments may already contain ranking history.
func removeRankingMigration(_ *gorm.DB) error {
	return nil
}
