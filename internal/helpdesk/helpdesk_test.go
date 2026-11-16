package helpdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestPayload(t *testing.T) {
	tests := []struct {
		m                  Message
		method, key, value string
	}{
		{Message{Text: "hello", Entities: json.RawMessage(`[{"type":"bold","offset":0,"length":5}]`)}, "sendMessage", "text", "hello"},
		{Message{Photo: []Media{{"small"}, {"large"}}, Caption: "说明"}, "sendPhoto", "photo", "large"},
		{Message{Video: &Media{"v"}}, "sendVideo", "video", "v"},
		{Message{Voice: &Media{"a"}}, "sendVoice", "voice", "a"},
		{Message{Document: &Media{"d"}}, "sendDocument", "document", "d"},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			method, p, err := payload(&tt.m)
			if err != nil || method != tt.method || p[tt.key] != tt.value {
				t.Fatalf("payload: %s %v %v", method, p, err)
			}
			if tt.m.Caption != "" && p["caption"] != tt.m.Caption {
				t.Fatal("caption lost")
			}
		})
	}
	if _, _, err := payload(&Message{}); err != ErrUnsupported {
		t.Fatal("unsupported must fail")
	}
}
func TestRetry(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404} {
		if retryable(&APIError{Code: code}) {
			t.Fatalf("%d retried", code)
		}
	}
	for _, code := range []int{429, 500, 503} {
		if !retryable(&APIError{Code: code}) {
			t.Fatalf("%d not retried", code)
		}
	}
	if retryDelay(1, &APIError{Code: 429, RetryAfter: 60}) != time.Minute {
		t.Fatal("retry_after ignored")
	}
	if retryable(permanent("expired")) {
		t.Fatal("permanent retried")
	}
}
func TestSecurityAndConfig(t *testing.T) {
	if equalSecret("", "x") || equalSecret("x", "y") || equalSecret("", "") || !equalSecret("x", "x") {
		t.Fatal("secret validation")
	}
	c := Config{WebhookSecret: "secret", AdminToken: "admin"}
	r := Router(c, nil)
	tests := []struct {
		path, method, body, secret string
		status                     int
	}{
		{"/webhook", "POST", `{}`, "", 403},
		{"/webhook", "POST", `{}`, "secret", 400},
		{"/webhook", "POST", `{"update_id":null}`, "secret", 400},
		{"/webhook", "POST", `{"update_id":-1}`, "secret", 400},
		{"/webhook", "POST", `{"update_id":1.5}`, "secret", 400},
		{"/webhook", "POST", `{"update_id":1}`, "secret", 500},
		{"/webhook", "POST", strings.Repeat("a", (1<<20)+1), "secret", 413},
		{"/admin/jobs", "GET", "", "", 401},
		{"/healthz", "GET", "", "", 200},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", tt.secret)
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		if res.Code != tt.status {
			t.Fatalf("%s: got %d want %d", tt.path, res.Code, tt.status)
		}
	}
	t.Setenv("BOT_TOKEN", "123:test")
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("WEBHOOK_SECRET", strings.Repeat("x", 32))
	t.Setenv("ADMIN_TOKEN", strings.Repeat("y", 32))
	t.Setenv("SUPPORT_GROUP_ID", "-10099")
	t.Setenv("BUSINESS_OWNER_IDS", "1,2")
	t.Setenv("AGENT_IDS", "3,4")
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_IDS", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("empty whitelist accepted")
	}
}
func TestCustomerFilter(t *testing.T) {
	m := Message{ID: 1, Date: time.Now().Unix(), BusinessID: "b", Chat: Chat{10, "private"}, From: &User{ID: 10}}
	if !validCustomer(&m) {
		t.Fatal("valid customer rejected")
	}
	m.SenderBusinessBot = &User{ID: 2}
	if validCustomer(&m) {
		t.Fatal("bot echo accepted")
	}
	m.SenderBusinessBot = nil
	m.From.ID = 5
	if validCustomer(&m) {
		t.Fatal("owner outgoing accepted")
	}
}
func TestTelegramRouting(t *testing.T) {
	for _, business := range []string{"", "connection"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/bot123:test/sendVoice" {
				t.Error(r.URL.Path)
			}
			var p map[string]any
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			if p["voice"] != "file" || p["chat_id"] != float64(9) {
				t.Error(p)
			}
			if business == "" {
				if p["message_thread_id"] != float64(7) || p["business_connection_id"] != nil {
					t.Error(p)
				}
			} else {
				if p["business_connection_id"] != business || p["message_thread_id"] != nil {
					t.Error(p)
				}
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
		}))
		api := &Telegram{Token: "123:test", Base: srv.URL, Client: srv.Client()}
		thread := int64(7)
		if business != "" {
			thread = 0
		}
		id, err := api.Send(context.Background(), &Message{Voice: &Media{"file"}}, 9, thread, business)
		srv.Close()
		if err != nil || id != 42 {
			t.Fatalf("%d %v", id, err)
		}
	}
}

// Set TEST_DATABASE_URL to an empty, disposable PostgreSQL database. This test truncates its tables.
func integrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	var name string
	if err := db.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, "_test") {
		pool.Close()
		t.Fatal("integration database name must end in _test; refusing destructive reset")
	}
	t.Cleanup(func() { pool.Close() })
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	if err = db.Exec("TRUNCATE jobs,routes RESTART IDENTITY").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE worker_states SET heartbeat_at = NULL").Error; err != nil {
		t.Fatal(err)
	}
	return db
}
func TestPostgresIntegration(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	var mu sync.Mutex
	counts := map[string]int{}
	deny := false
	enabled := true
	reply := true
	owner := int64(1)
	flood := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		counts[method]++
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getBusinessConnection":
			fmt.Fprintf(w, `{"ok":true,"result":{"id":"conn","user":{"id":%d},"is_enabled":%t,"rights":{"can_reply":%t}}}`, owner, enabled, reply)
		case "createForumTopic":
			fmt.Fprint(w, `{"ok":true,"result":{"message_thread_id":88}}`)
		default:
			if p["business_connection_id"] != nil && flood {
				fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"rate limited","parameters":{"retry_after":20}}`)
				return
			}
			if p["business_connection_id"] != nil && deny {
				fmt.Fprint(w, `{"ok":false,"error_code":403,"description":"blocked"}`)
				return
			}
			if p["business_connection_id"] != nil && (p["chat_id"] != float64(10) || p["business_connection_id"] != "conn" || p["message_thread_id"] != nil) {
				t.Error("invalid customer route", p)
			}
			if p["message_thread_id"] != nil && (p["chat_id"] != float64(-10099) || p["message_thread_id"] != float64(88)) {
				t.Error("invalid forum route", p)
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
		}
	}))
	defer srv.Close()
	cfg := Config{GroupID: -10099, Owners: map[int64]bool{1: true}, Agents: map[int64]bool{2: true}, MaxAttempts: 3, WebhookSecret: "secret", AdminToken: "admin"}
	worker := &Worker{DB: db, API: &Telegram{Token: "123:test", Base: srv.URL, Client: srv.Client()}, Config: cfg}
	customer := func(id int64) Update {
		return Update{ID: id, BusinessMessage: &Message{ID: id, Date: time.Now().Unix(), BusinessID: "conn", Chat: Chat{10, "private"}, From: &User{ID: 10}, Text: "你好"}}
	}
	staff := func(id, agent int64) Update {
		return Update{ID: id, Message: &Message{ID: id, ThreadID: 88, Chat: Chat{-10099, "supergroup"}, From: &User{ID: agent}, Text: "答复"}}
	}
	enqueue := func(u Update) {
		t.Helper()
		b, _ := json.Marshal(u)
		if err := Enqueue(ctx, db, u, b); err != nil {
			t.Fatal(err)
		}
	}
	step := func() {
		t.Helper()
		if processed, err := worker.Step(ctx, db); err != nil || !processed {
			t.Fatalf("step: %t %v", processed, err)
		}
	}
	status := func(id int64, want string) {
		t.Helper()
		var j Job
		if err := db.First(&j, "update_id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if j.Attempts < 1 {
			t.Fatal("attempt not persisted")
		}
		if j.Status != want {
			t.Fatalf("job %d: %s %s", id, j.Status, j.LastError)
		}
	}
	// Concurrent webhook retries and alternate update IDs for one source must collapse to one row.
	u := customer(1)
	b, _ := json.Marshal(u)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Enqueue(ctx, db, u, b); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	same := u
	same.ID = 999
	enqueue(same)
	var n int64
	db.Model(&Job{}).Count(&n)
	if n != 1 {
		t.Fatalf("dedup rows=%d", n)
	}
	step()
	status(1, "done")
	enqueue(customer(2))
	step()
	if counts["createForumTopic"] != 1 {
		t.Fatal("duplicate topic")
	}
	enqueue(staff(3, 2))
	step()
	status(3, "done")
	calls := counts["sendMessage"]
	enqueue(staff(4, 999))
	step()
	status(4, "ignored")
	if counts["sendMessage"] != calls {
		t.Fatal("unauthorized sender delivered")
	}
	anonymous := staff(5, 2)
	anonymous.Message.SenderChat = &Chat{-10099, "supergroup"}
	enqueue(anonymous)
	step()
	status(5, "ignored")
	// Window and revoked permissions create dead letters, with no customer send.
	db.Model(&Route{}).Where("customer_id = 10").Update("last_incoming", time.Now().Add(-25*time.Hour))
	enqueue(staff(6, 2))
	step()
	status(6, "dead")
	enqueue(customer(7))
	step()
	mu.Lock()
	reply = false
	mu.Unlock()
	enqueue(staff(8, 2))
	step()
	status(8, "dead")
	mu.Lock()
	reply = true
	enabled = false
	mu.Unlock()
	enqueue(staff(9, 2))
	step()
	status(9, "dead")
	mu.Lock()
	enabled = true
	owner = 999
	mu.Unlock()
	enqueue(staff(10, 2))
	step()
	status(10, "dead")
	mu.Lock()
	owner = 1
	flood = true
	mu.Unlock()
	enqueue(staff(11, 2))
	step()
	status(11, "pending")
	var job Job
	db.First(&job, "update_id = 11")
	if time.Until(job.NextAt) < 18*time.Second {
		t.Fatal("retry_after not persisted")
	}
	// New worker object resumes durable pending work after a simulated restart.
	mu.Lock()
	flood = false
	mu.Unlock()
	db.Model(&Job{}).Where("update_id = 11").Update("next_at", time.Now().Add(-time.Second))
	restarted := *worker
	worker = &restarted
	step()
	status(11, "done")
	mu.Lock()
	deny = true
	mu.Unlock()
	enqueue(staff(12, 2))
	step()
	status(12, "dead")
	router := Router(cfg, db)
	req := httptest.NewRequest("POST", "/admin/jobs/12/retry", nil)
	req.Header.Set("Authorization", "Bearer admin")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != 202 {
		t.Fatal(res.Code)
	}
	mu.Lock()
	deny = false
	mu.Unlock()
	step()
	status(12, "done")
	req = httptest.NewRequest("POST", "/admin/jobs/12/retry", nil)
	req.Header.Set("Authorization", "Bearer admin")
	res = httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != 409 {
		t.Fatal("done job replay allowed")
	}
	req = httptest.NewRequest("POST", "/webhook", strings.NewReader(`{"update_id":50}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret")
	res = httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatal(res.Code)
	}
	step()
	status(50, "ignored")
	// Exhaust retry budget; independent jobs remain processable.
	mu.Lock()
	flood = true
	mu.Unlock()
	enqueue(staff(51, 2))
	step()
	status(51, "pending")
	for i := 0; i < 2; i++ {
		db.Model(&Job{}).Where("update_id = 51").Update("next_at", time.Now().Add(-time.Second))
		step()
	}
	status(51, "dead")
	job = Job{}
	db.First(&job, "update_id = 51")
	if job.Attempts != 3 {
		t.Fatalf("wrong attempt budget: %d", job.Attempts)
	}
	mu.Lock()
	flood = false
	mu.Unlock()
	// Service events from whitelisted humans must never be relayed or produce dead letters.
	service := []byte(`{"update_id":52,"message":{"message_id":52,"message_thread_id":88,"chat":{"id":-10099,"type":"supergroup"},"from":{"id":2},"forum_topic_closed":{}}}`)
	var serviceUpdate Update
	_ = json.Unmarshal(service, &serviceUpdate)
	if !serviceUpdate.Message.IsService {
		t.Fatal("service not detected")
	}
	if err := Enqueue(ctx, db, serviceUpdate, service); err != nil {
		t.Fatal(err)
	}
	step()
	status(52, "ignored")
	// The same PostgreSQL advisory lock cannot be held by two independent sessions.
	err := db.Connection(func(a *gorm.DB) error {
		if err := a.Exec("SELECT pg_advisory_lock(7632142)").Error; err != nil {
			return err
		}
		defer a.Exec("SELECT pg_advisory_unlock(7632142)")
		return db.Connection(func(b *gorm.DB) error {
			var locked bool
			if err := b.Raw("SELECT pg_try_advisory_lock(7632142)").Scan(&locked).Error; err != nil {
				return err
			}
			if locked {
				t.Fatal("second worker acquired lock")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTelegramErrors(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
		code   int
	}{
		{429, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`, 429},
		{502, `upstream unavailable`, 502},
		{200, `{"ok":false,"error_code":403,"description":"forbidden"}`, 403},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); fmt.Fprint(w, tt.body) }))
		api := &Telegram{Token: "123:secret", Base: srv.URL, Client: srv.Client()}
		err := api.Call(context.Background(), "sendMessage", map[string]any{}, nil)
		srv.Close()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != tt.code {
			t.Fatalf("expected %d: %v", tt.code, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	api := &Telegram{Token: "123:secret", Base: srv.URL, Client: srv.Client()}
	if err := api.Call(context.Background(), "sendMessage", map[string]any{}, nil); err == nil || strings.Contains(err.Error(), "123:secret") {
		t.Fatal("transport error leaks token or is missing", err)
	}
}

func TestWorkerFailover(t *testing.T) {
	db := integrationDB(t)
	cfg := Config{MaxAttempts: 3, PollInterval: 10 * time.Millisecond}
	first := &Worker{DB: db, Config: cfg}
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	done1 := make(chan struct{})
	go func() { defer close(done1); first.Run(ctx1) }()
	waitUntil := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("worker did not progress")
	}
	waitUntil(func() bool {
		var held bool
		_ = db.Raw("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=7632142)").Scan(&held).Error
		return held
	})
	second := &Worker{DB: db, Config: cfg}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan struct{})
	go func() { defer close(done2); second.Run(ctx2) }()
	cancel1()
	select {
	case <-done1:
	case <-time.After(3 * time.Second):
		t.Fatal("first worker did not stop")
	}
	raw := []byte(`{"update_id":100}`)
	if err := Enqueue(context.Background(), db, Update{ID: 100}, raw); err != nil {
		t.Fatal(err)
	}
	waitUntil(func() bool { var j Job; return db.First(&j, "update_id = 100").Error == nil && j.Status == "ignored" })
	cancel2()
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatal("second worker did not stop")
	}
}

func TestScheduledMessageIsNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{"message_id":0}}`) }))
	defer srv.Close()
	api := &Telegram{Token: "123:test", Base: srv.URL, Client: srv.Client()}
	id, err := api.Send(context.Background(), &Message{Video: &Media{"video"}}, -10099, 88, "")
	if err != nil || id != 0 {
		t.Fatalf("scheduled send treated as failure: %d %v", id, err)
	}
}
