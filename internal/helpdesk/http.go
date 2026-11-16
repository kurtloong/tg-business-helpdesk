package helpdesk

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func equalSecret(got, want string) bool {
	return len(want) > 0 && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
func Router(c Config, db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Gin's default recovery dumps request headers, including webhook secrets.
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(g *gin.Context, _ any) {
		slog.Error("http handler panic", "stack", string(debug.Stack()))
		g.AbortWithStatus(500)
	}))
	_ = r.SetTrustedProxies(nil)
	r.GET("/healthz", func(g *gin.Context) { g.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(g *gin.Context) {
		ctx, cancel := context.WithTimeout(g.Request.Context(), 3*time.Second)
		defer cancel()
		_, healthy, err := workerHealth(ctx, db)
		if err != nil || !healthy {
			g.JSON(503, gin.H{"status": "not_ready"})
			return
		}
		g.JSON(200, gin.H{"status": "ready"})
	})
	r.POST("/webhook", func(g *gin.Context) {
		if !equalSecret(g.GetHeader("X-Telegram-Bot-Api-Secret-Token"), c.WebhookSecret) {
			g.Status(403)
			return
		}
		g.Request.Body = http.MaxBytesReader(g.Writer, g.Request.Body, 1<<20)
		raw, err := io.ReadAll(g.Request.Body)
		if err != nil {
			g.Status(413)
			return
		}
		var u Update
		var meta map[string]json.RawMessage
		if json.Unmarshal(raw, &u) != nil || json.Unmarshal(raw, &meta) != nil || meta["update_id"] == nil || string(meta["update_id"]) == "null" || u.ID < 0 {
			g.Status(400)
			return
		}
		if err = Enqueue(g.Request.Context(), db, u, raw); err != nil {
			slog.Error("webhook enqueue failed", "update_id", u.ID, "error_class", errorClass(err))
			g.Status(503)
			return
		}
		g.Status(200)
	})
	admin := r.Group("/admin")
	admin.Use(func(g *gin.Context) {
		if !equalSecret(g.GetHeader("Authorization"), "Bearer "+c.AdminToken) {
			g.AbortWithStatus(401)
			return
		}
		g.Next()
	})
	admin.GET("/status", func(g *gin.Context) {
		ctx, cancel := context.WithTimeout(g.Request.Context(), 3*time.Second)
		defer cancel()
		status, err := LoadStatus(ctx, db)
		if err != nil {
			slog.Error("status query failed", "error_class", errorClass(err))
			g.Status(503)
			return
		}
		g.JSON(200, status)
	})
	admin.GET("/jobs", func(g *gin.Context) {
		state := g.DefaultQuery("status", "dead")
		if state != "dead" && state != "pending" && state != "done" && state != "ignored" {
			g.Status(400)
			return
		}
		var jobs []Job
		if err := db.WithContext(g.Request.Context()).Select("update_id,source_key,status,attempts,next_at,last_error,result_message_id,created_at,updated_at").Where("status = ?", state).Order("updated_at DESC").Limit(100).Find(&jobs).Error; err != nil {
			g.Status(503)
			return
		}
		g.JSON(200, jobs)
	})
	admin.POST("/jobs/:id/retry", func(g *gin.Context) {
		id, err := strconv.ParseInt(g.Param("id"), 10, 64)
		if err != nil || id < 0 {
			g.Status(400)
			return
		}
		res := db.WithContext(g.Request.Context()).Model(&Job{}).Where("update_id = ? AND status = 'dead'", id).Updates(map[string]any{"status": "pending", "attempts": 0, "last_error": "", "next_at": time.Now().UTC()})
		if res.Error != nil {
			g.Status(503)
			return
		}
		if res.RowsAffected == 0 {
			g.Status(409)
			return
		}
		g.Status(202)
	})
	return r
}
func RegisterWebhook(c Config, api *Telegram, ctx context.Context) error {
	if !strings.HasPrefix(c.WebhookURL, "https://") {
		return errors.New("WEBHOOK_URL must start with https://")
	}
	return api.Call(ctx, "setWebhook", map[string]any{"url": c.WebhookURL, "secret_token": c.WebhookSecret, "max_connections": 1, "allowed_updates": []string{"message", "business_connection", "business_message"}, "drop_pending_updates": false}, nil)
}
