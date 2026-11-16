package helpdesk

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

const WorkerStaleAfter = 90 * time.Second

type workerState struct {
	ID          int        `gorm:"primaryKey"`
	HeartbeatAt *time.Time `json:"heartbeat_at"`
}

func heartbeat(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Exec("UPDATE worker_states SET heartbeat_at = now() WHERE id = 1").Error
}

func workerHealth(ctx context.Context, db *gorm.DB) (*time.Time, bool, error) {
	var state workerState
	err := db.WithContext(ctx).First(&state, "id = 1").Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return state.HeartbeatAt, state.HeartbeatAt != nil && time.Since(*state.HeartbeatAt) < WorkerStaleAfter, nil
}

type RuntimeStatus struct {
	WorkerHealthy        bool             `json:"worker_healthy"`
	HeartbeatAt          *time.Time       `json:"heartbeat_at"`
	Queue                map[string]int64 `json:"queue"`
	ReadyJobs            int64            `json:"ready_jobs"`
	OldestPendingAt      *time.Time       `json:"oldest_pending_at"`
	OldestPendingSeconds float64          `json:"oldest_pending_seconds"`
	OldestReadySeconds   float64          `json:"oldest_ready_seconds"`
}

// LoadStatus includes all queued/dead jobs, rather than only the 100 rows shown in the job list.
func LoadStatus(ctx context.Context, db *gorm.DB) (RuntimeStatus, error) {
	var result RuntimeStatus
	at, healthy, err := workerHealth(ctx, db)
	if err != nil {
		return result, err
	}
	result.HeartbeatAt = at
	result.WorkerHealthy = healthy
	var counts struct {
		Pending       int64
		Dead          int64
		Ready         int64
		OldestPending sql.NullTime
		OldestReady   sql.NullTime
	}
	err = db.WithContext(ctx).Raw(`SELECT
 COUNT(*) FILTER (WHERE status='pending') AS pending,
 COUNT(*) FILTER (WHERE status='dead') AS dead,
 COUNT(*) FILTER (WHERE status='pending' AND next_at <= now()) AS ready,
 MIN(created_at) FILTER (WHERE status='pending') AS oldest_pending,
 MIN(created_at) FILTER (WHERE status='pending' AND next_at <= now()) AS oldest_ready
 FROM jobs WHERE status IN ('pending','dead')`).Scan(&counts).Error
	if err != nil {
		return result, err
	}
	result.Queue = map[string]int64{"pending": counts.Pending, "dead": counts.Dead}
	result.ReadyJobs = counts.Ready
	if counts.OldestPending.Valid {
		result.OldestPendingAt = &counts.OldestPending.Time
		result.OldestPendingSeconds = max(0, time.Since(counts.OldestPending.Time).Seconds())
	}
	if counts.OldestReady.Valid {
		result.OldestReadySeconds = max(0, time.Since(counts.OldestReady.Time).Seconds())
	}
	return result, nil
}

// Log classification preserves useful database diagnostics without serializing SQL, rows or a DSN.
func errorClass(err error) string {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		return "postgres_" + coded.SQLState()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "database_unavailable"
}
