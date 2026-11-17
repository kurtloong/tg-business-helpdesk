package helpdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Worker struct {
	DB     *gorm.DB
	API    *Telegram
	Config Config
}

var errIgnored = errors.New("ignored")

type permanentError struct{ reason string }

func (e *permanentError) Error() string { return e.reason }
func permanent(s string) error          { return &permanentError{s} }
func validCustomer(m *Message) bool {
	return m != nil && !m.IsService && m.BusinessID == "" && m.Chat.Type == "private" && m.Chat.ID > 0 && m.From != nil && !m.From.IsBot && m.From.ID == m.Chat.ID && m.SenderChat == nil && m.SenderBusinessBot == nil && m.Date > 0 && m.ID > 0
}

// The support forum must never be reinterpreted as a customer group.
func (w *Worker) customerMessage(m *Message) bool {
	if validCustomer(m) {
		return true
	}
	return m != nil && !m.IsService && m.BusinessID == "" && m.SenderBusinessBot == nil &&
		m.Chat.ID < 0 && m.Chat.ID != w.Config.GroupID &&
		(m.Chat.Type == "group" || m.Chat.Type == "supergroup") && m.ThreadID >= 0 &&
		m.ID > 0 && m.Date > 0 && m.From != nil && m.From.ID > 0 && !m.From.IsBot && m.SenderChat == nil
}
func customerThread(m *Message) int64 {
	if m.Chat.ID < 0 && m.IsTopicMessage {
		return m.ThreadID
	}
	return 0
}
func (w *Worker) handle(ctx context.Context, db *gorm.DB, j *Job) (int64, error) {
	var u Update
	if err := json.Unmarshal([]byte(j.Payload), &u); err != nil {
		return 0, permanent("invalid stored update")
	}
	if m := u.Message; w.customerMessage(m) {
		var err error
		var route Route
		err = db.Where("customer_id = ? AND customer_thread_id = ?", m.Chat.ID, customerThread(m)).First(&route).Error
		incoming := time.Unix(m.Date, 0).UTC()
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			route = Route{CustomerID: m.Chat.ID, CustomerThreadID: customerThread(m), GroupID: w.Config.GroupID, LastIncoming: incoming}
			if err = db.Create(&route).Error; err != nil {
				return 0, err
			}
		} else if incoming.After(route.LastIncoming) {
			if err = db.Model(&route).Update("last_incoming", gorm.Expr("GREATEST(last_incoming, ?)", incoming)).Error; err != nil {
				return 0, err
			}
		}
		if route.GroupID != w.Config.GroupID {
			return 0, permanent("support group changed; explicit route migration required")
		}
		if route.ThreadID == 0 {
			var topic struct {
				ThreadID int64 `json:"message_thread_id"`
			}
			// Stable numeric titles avoid impersonation through customer-controlled names.
			name := fmt.Sprintf("客户%d", m.Chat.ID)
			if m.Chat.ID < 0 {
				name = fmt.Sprintf("客户群%d", m.Chat.ID)
				if route.CustomerThreadID > 0 {
					name += fmt.Sprintf(" 话题%d", route.CustomerThreadID)
				}
			}
			if err = w.API.Call(ctx, "createForumTopic", map[string]any{"chat_id": route.GroupID, "name": name}, &topic); err != nil {
				return 0, err
			}
			if topic.ThreadID <= 0 {
				return 0, errors.New("Telegram returned no topic ID")
			}
			if err = db.Model(&route).Update("thread_id", topic.ThreadID).Error; err != nil {
				return 0, err
			}
		}
		if _, _, err = payload(m); errors.Is(err, ErrUnsupported) {
			// Preserve visibility of unsupported input without silently dropping a customer contact.
			m = &Message{Text: fmt.Sprintf("[不支持的客户消息类型] chat=%d message=%d；请客户改发文本或文件。", m.Chat.ID, m.ID)}
		}
		return w.API.Send(ctx, m, route.GroupID, route.ThreadID)
	}
	m := u.Message
	if m == nil || m.BusinessID != "" || m.SenderBusinessBot != nil || m.ID <= 0 || m.IsService || m.Chat.ID != w.Config.GroupID || m.Chat.Type != "supergroup" || m.ThreadID <= 0 || m.From == nil || m.From.IsBot || m.SenderChat != nil || !w.Config.Agents[m.From.ID] {
		return 0, errIgnored
	}
	var route Route
	if err := db.Where("group_id = ? AND thread_id = ?", m.Chat.ID, m.ThreadID).First(&route).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, errIgnored
		}
		return 0, err
	}
	if _, _, err := payload(m); err != nil {
		return 0, permanent("unsupported staff message type")
	}
	return w.API.Send(ctx, m, route.CustomerID, route.CustomerThreadID)
}
func retryDelay(attempt int, err error) time.Duration {
	d := time.Second * time.Duration(1<<min(attempt, 8))
	var api *APIError
	if errors.As(err, &api) && api.RetryAfter > 0 && time.Duration(api.RetryAfter)*time.Second > d {
		d = time.Duration(api.RetryAfter) * time.Second
	}
	return d
}
func retryable(err error) bool {
	var p *permanentError
	if errors.As(err, &p) || errors.Is(err, ErrUnsupported) {
		return false
	}
	var api *APIError
	if errors.As(err, &api) {
		return api.Temporary()
	}
	return true
}

// Step must run under the worker's session advisory lock. Each checkpoint commits independently.
func (w *Worker) Step(ctx context.Context, db *gorm.DB) (bool, error) {
	started := time.Now()
	var j Job
	err := db.Where("status = 'pending' AND next_at <= ?", time.Now().UTC()).Order("update_id").First(&j).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Persist attempt before network I/O so a crash does not reset the attempt budget.
	j.Attempts++
	if err = db.Model(&j).Updates(map[string]any{"attempts": j.Attempts}).Error; err != nil {
		return false, err
	}
	var messageID int64
	if j.Attempts > w.Config.MaxAttempts {
		err = permanent("attempt budget exhausted after interrupted processing")
	} else {
		messageID, err = w.handle(ctx, db, &j)
	}
	status := "done"
	reason := ""
	next := time.Now().UTC()
	if errors.Is(err, errIgnored) {
		status = "ignored"
	} else if err != nil {
		reason = err.Error()
		if w.Config.Token != "" {
			reason = strings.ReplaceAll(reason, w.Config.Token, "[redacted]")
		}
		status = "dead"
		if retryable(err) && j.Attempts < w.Config.MaxAttempts {
			status = "pending"
			next = next.Add(retryDelay(j.Attempts, err))
		}
	}
	// Error text never contains a bot-token URL or message body.
	if e := db.Model(&j).Updates(map[string]any{"status": status, "next_at": next, "last_error": reason, "result_message_id": messageID}).Error; e != nil {
		return true, e
	}
	direction := "event"
	var update Update
	if json.Unmarshal([]byte(j.Payload), &update) == nil {
		if w.customerMessage(update.Message) {
			direction = "customer_to_staff"
		} else if update.Message != nil {
			direction = "staff_to_customer"
		}
	}
	attrs := []any{"update_id", j.UpdateID, "status", status, "attempt", j.Attempts, "direction", direction, "duration_ms", time.Since(started).Milliseconds(), "result_message_id", messageID}
	if status == "ignored" {
		slog.Debug("job completed", attrs...)
	} else if status == "pending" {
		attrs = append(attrs, "reason", reason, "next_at", next)
		slog.Warn("job retry scheduled", attrs...)
	} else if status == "done" {
		slog.Info("job completed", attrs...)
	}
	if status == "dead" {
		slog.Error("delivery failed", "update_id", j.UpdateID, "reason", reason)
		// Best-effort operator alert; the durable dead-letter record is authoritative.
		_, alertErr := w.API.Send(ctx, &Message{Text: fmt.Sprintf("任务 %d（%s）发送失败：%s。请管理员检查死信队列。", j.UpdateID, j.SourceKey, reason)}, w.Config.GroupID, 0)
		if alertErr != nil {
			slog.Warn("operator alert failed", "update_id", j.UpdateID)
		}
	}
	return true, nil
}
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := w.DB.WithContext(ctx).Connection(func(db *gorm.DB) error {
			// Connection callbacks share a mutable statement; clone per query to avoid stale predicates.
			db = db.Session(&gorm.Session{NewDB: true})
			var locked bool
			if err := db.Raw("SELECT pg_try_advisory_lock(7632142)").Scan(&locked).Error; err != nil {
				return err
			}
			if !locked {
				return nil
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
				defer stop()
				if err := db.WithContext(cleanup).Exec("UPDATE worker_states SET heartbeat_at = NULL WHERE id = 1").Error; err != nil {
					slog.Warn("worker heartbeat cleanup failed", "error_class", errorClass(err))
				}
				if err := db.WithContext(cleanup).Exec("SELECT pg_advisory_unlock(7632142)").Error; err != nil {
					slog.Warn("worker leadership unlock failed", "error_class", errorClass(err))
				}
			}()
			slog.Info("worker acquired leadership")
			lastHeartbeat := time.Time{}
			for ctx.Err() == nil {
				if time.Since(lastHeartbeat) >= 5*time.Second {
					if err := heartbeat(ctx, db); err != nil {
						return err
					}
					lastHeartbeat = time.Now()
				}
				processed, err := w.Step(ctx, db)
				if err != nil {
					return err
				}
				// Only an empty queue sleeps. Telegram.Send separately limits real outbound messages.
				if processed {
					continue
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(w.Config.PollInterval):
				}
			}
			return ctx.Err()
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("worker database error; reconnecting", "error_class", errorClass(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
