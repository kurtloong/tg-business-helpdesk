package helpdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnqueueKeepsCustomerWindowFreshBeforeDelivery(t *testing.T) {
	db := integrationDB(t)
	route := Route{BusinessID: "conn", CustomerID: 10, GroupID: -10099, ThreadID: 88, LastIncoming: time.Now().Add(-25 * time.Hour)}
	if err := db.Create(&route).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	u := Update{ID: 1, BusinessMessage: &Message{ID: 1, Date: now.Unix(), BusinessID: "conn", From: &User{ID: 10}, Chat: Chat{ID: 10, Type: "private"}, Text: "new customer message"}}
	raw, _ := json.Marshal(u)
	if err := Enqueue(context.Background(), db, u, raw); err != nil {
		t.Fatal(err)
	}
	var saved Route
	if err := db.First(&saved, route.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.LastIncoming.Equal(now) {
		t.Fatalf("new customer input did not refresh reply window: %s", saved.LastIncoming)
	}
	u.ID = 2
	u.BusinessMessage.ID = 2
	u.BusinessMessage.Date = now.Add(-time.Hour).Unix()
	raw, _ = json.Marshal(u)
	if err := Enqueue(context.Background(), db, u, raw); err != nil {
		t.Fatal(err)
	}
	saved = Route{}
	if err := db.First(&saved, route.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.LastIncoming.Equal(now) {
		t.Fatal("out-of-order input shortened reply window")
	}
}

func TestRuntimeStatusAndReadiness(t *testing.T) {
	db := integrationDB(t)
	config := Config{AdminToken: "test-admin"}
	router := Router(config, db)
	request := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	if got := request("/readyz", "").Code; got != 503 {
		t.Fatalf("worker absent should not be ready: %d", got)
	}
	if got := request("/admin/status", "").Code; got != 401 {
		t.Fatal(got)
	}
	if err := heartbeat(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := request("/readyz", "").Code; got != 200 {
		t.Fatalf("live worker should be ready: %d", got)
	}
	now := time.Now().UTC()
	jobs := []Job{
		{UpdateID: 1, SourceKey: "1", Payload: `{"private":"body"}`, Status: "pending", NextAt: now.Add(-time.Minute), CreatedAt: now.Add(-2 * time.Minute)},
		{UpdateID: 2, SourceKey: "2", Payload: `{}`, Status: "pending", NextAt: now.Add(time.Hour)},
		{UpdateID: 3, SourceKey: "3", Payload: `{}`, Status: "dead", NextAt: now},
	}
	if err := db.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	result := request("/admin/status", "test-admin")
	if result.Code != 200 {
		t.Fatal(result.Code)
	}
	var status RuntimeStatus
	if err := json.Unmarshal(result.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.WorkerHealthy || status.Queue["pending"] != 2 || status.Queue["dead"] != 1 || status.ReadyJobs != 1 || status.OldestReadySeconds < 119 {
		t.Fatalf("inaccurate status: %+v", status)
	}
	if strings.Contains(result.Body.String(), "private") || strings.Contains(result.Body.String(), "test-admin") {
		t.Fatal("status exposes body or secret")
	}
	if err := db.Exec("UPDATE worker_states SET heartbeat_at = now() - interval '2 minutes'").Error; err != nil {
		t.Fatal(err)
	}
	if got := request("/readyz", "").Code; got != 503 {
		t.Fatalf("stalled worker reported ready: %d", got)
	}
}

func TestIgnoredBacklogDrainsWithoutSendThrottle(t *testing.T) {
	db := integrationDB(t)
	for i := int64(1); i <= 20; i++ {
		u := Update{ID: i}
		raw, _ := json.Marshal(u)
		if err := Enqueue(context.Background(), db, u, raw); err != nil {
			t.Fatal(err)
		}
	}
	worker := &Worker{DB: db, Config: Config{MaxAttempts: 3, PollInterval: 3 * time.Second}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		if err := db.Model(&Job{}).Where("status = 'ignored'").Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n == 20 {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("irrelevant updates waited for outbound send interval")
}

func TestOutboundSendThrottleAndCancellation(t *testing.T) {
	times := make(chan time.Time, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times <- time.Now()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()
	api := &Telegram{Token: "123:test", Base: server.URL, Client: server.Client(), SendInterval: 200 * time.Millisecond, PrivateSendInterval: 200 * time.Millisecond}
	for i := 0; i < 2; i++ {
		if _, err := api.Send(context.Background(), &Message{Text: "x"}, 1, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-times, <-times
	if second.Sub(first) < 180*time.Millisecond {
		t.Fatalf("outbound interval bypassed: %s", second.Sub(first))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := api.Send(ctx, &Message{Text: "x"}, 1, 0, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if len(times) > 0 {
		t.Fatal("canceled send reached Telegram")
	}
}

func TestUnapprovedBusinessOwnerCannotSpamStaffAlerts(t *testing.T) {
	db := integrationDB(t)
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getBusinessConnection") {
			fmt.Fprint(w, `{"ok":true,"result":{"id":"unapproved","user":{"id":999},"is_enabled":true}}`)
			return
		}
		sends.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()
	worker := &Worker{DB: db, API: &Telegram{Token: "123:test", Base: server.URL, Client: server.Client()}, Config: Config{Owners: map[int64]bool{1: true}, MaxAttempts: 3}}
	u := Update{ID: 1, BusinessMessage: &Message{ID: 1, Date: time.Now().Unix(), BusinessID: "unapproved", Chat: Chat{ID: 10, Type: "private"}, From: &User{ID: 10}, Text: "not our account"}}
	raw, _ := json.Marshal(u)
	if err := Enqueue(context.Background(), db, u, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Step(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var job Job
	if err := db.First(&job, "update_id=1").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "ignored" || sends.Load() != 0 {
		t.Fatalf("unauthorized account generated staff traffic: %s sends=%d", job.Status, sends.Load())
	}
}

func TestGroupThrottleDoesNotDelayCustomerReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`) }))
	defer server.Close()
	api := &Telegram{Token: "123:test", Base: server.URL, Client: server.Client(), SendInterval: 5 * time.Second, PrivateSendInterval: time.Second}
	if _, err := api.Send(context.Background(), &Message{Text: "inbound mirror"}, -10099, 88, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := api.Send(ctx, &Message{Text: "customer reply"}, 10, 0, "conn"); err != nil {
		t.Fatal("customer reply waited for the group's cooldown", err)
	}
}
