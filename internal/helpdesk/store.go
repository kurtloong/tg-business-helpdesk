package helpdesk

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"tg-business-helpdesk/migrations"
)

func OpenDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("database connection failed")
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}
func Migrate(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7632141)").Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())").Error; err != nil {
			return err
		}
		for _, migration := range []struct {
			version int
			sql     string
		}{{1, migrations.Initial}, {2, migrations.Observability}, {3, migrations.BotRoutes}} {
			var count int64
			if err := tx.Table("schema_migrations").Where("version = ?", migration.version).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			if err := tx.Exec(migration.sql).Error; err != nil {
				return err
			}
			if err := tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)", migration.version).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func sourceKey(u Update) string {
	if m := u.Message; m != nil {
		prefix := "g"
		if m.Chat.Type == "private" {
			prefix = "p"
		}
		return fmt.Sprintf("%s:%d:%d", prefix, m.Chat.ID, m.ID)
	}
	return fmt.Sprintf("u:%d", u.ID)
}
func Enqueue(ctx context.Context, db *gorm.DB, u Update, raw []byte) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Job{UpdateID: u.ID, SourceKey: sourceKey(u), Payload: string(raw), Status: "pending", NextAt: time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		// Retain the latest contact timestamp, including while topic forwarding is waiting for a retry.
		if result.RowsAffected > 0 && validCustomer(u.Message) {
			m := u.Message
			return tx.Model(&Route{}).Where("customer_id = ?", m.Chat.ID).
				Update("last_incoming", gorm.Expr("GREATEST(last_incoming, ?)", time.Unix(m.Date, 0).UTC())).Error
		}
		return nil
	})
}
