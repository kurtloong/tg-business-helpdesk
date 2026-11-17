package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tg-business-helpdesk/internal/helpdesk"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	action := flag.String("action", "serve", "serve, migrate, set-webhook, webhook-info, healthcheck, or check")
	flag.Parse()
	if err := run(*action); err != nil {
		slog.Error("startup failed", "reason", err)
		os.Exit(1)
	}
}
func run(action string) error {
	c, err := helpdesk.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	api := helpdesk.NewTelegram(c)
	if action == "healthcheck" {
		return healthcheck(ctx, c.Listen)
	}
	if action == "webhook-info" {
		var info json.RawMessage
		if err = api.Call(ctx, "getWebhookInfo", map[string]any{}, &info); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(info)
	}
	if action == "set-webhook" {
		return helpdesk.RegisterWebhook(c, api, ctx)
	}
	if action == "check" {
		botID, err := api.Check(ctx, c.GroupID)
		if err != nil {
			return err
		}
		slog.Info("Telegram prerequisites passed", "bot_id", botID)
		return nil
	}
	if action != "serve" && action != "migrate" {
		return errors.New("unknown action")
	}
	db, err := helpdesk.OpenDB(c.DSN)
	if err != nil {
		return err
	}
	pool, _ := db.DB()
	defer pool.Close()
	if err = helpdesk.Migrate(db); err != nil {
		return errors.New("database migration failed")
	}
	if action == "migrate" {
		slog.Info("database migrated")
		return nil
	}
	worker := &helpdesk.Worker{DB: db, API: api, Config: c}
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	server := &http.Server{Addr: c.Listen, Handler: helpdesk.Router(c, db), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	slog.Info("server started", "listen", c.Listen)
	select {
	case <-ctx.Done():
	case err = <-serverErrors:
		cancel()
	}
	shutdown, stop := context.WithTimeout(context.Background(), 25*time.Second)
	defer stop()
	if shutdownErr := server.Shutdown(shutdown); shutdownErr != nil {
		slog.Error("http shutdown incomplete")
		_ = server.Close()
	}
	select {
	case <-done:
	case <-shutdown.Done():
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func healthcheck(ctx context.Context, listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return errors.New("invalid LISTEN_ADDR")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("readiness probe failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("worker or database is not ready")
	}
	return nil
}
