package helpdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnqueueTracksLatestContactBeforeDelivery(t *testing.T) {
	db := integrationDB(t)
	route := Route{CustomerID: 10, GroupID: -10099, ThreadID: 88, LastIncoming: time.Now().Add(-25 * time.Hour)}
	if err := db.Create(&route).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	u := Update{ID: 1, Message: &Message{ID: 1, Date: now.Unix(), From: &User{ID: 10}, Chat: Chat{ID: 10, Type: "private"}, Text: "new customer message"}}
	raw, _ := json.Marshal(u)
	if err := Enqueue(context.Background(), db, u, raw); err != nil {
		t.Fatal(err)
	}
	var saved Route
	if err := db.First(&saved, route.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.LastIncoming.Equal(now) {
		t.Fatalf("new customer input did not refresh contact timestamp: %s", saved.LastIncoming)
	}
	u.ID = 2
	u.Message.ID = 2
	u.Message.Date = now.Add(-time.Hour).Unix()
	raw, _ = json.Marshal(u)
	if err := Enqueue(context.Background(), db, u, raw); err != nil {
		t.Fatal(err)
	}
	saved = Route{}
	if err := db.First(&saved, route.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.LastIncoming.Equal(now) {
		t.Fatal("out-of-order input shortened contact timestamp")
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
		if _, err := api.Send(context.Background(), &Message{Text: "x"}, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-times, <-times
	if second.Sub(first) < 180*time.Millisecond {
		t.Fatalf("outbound interval bypassed: %s", second.Sub(first))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := api.Send(ctx, &Message{Text: "x"}, 1, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if len(times) > 0 {
		t.Fatal("canceled send reached Telegram")
	}
}

func TestLegacyBusinessUpdateIsIgnored(t *testing.T) {
	db := integrationDB(t)
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()
	worker := &Worker{DB: db, API: &Telegram{Token: "123:test", Base: server.URL, Client: server.Client()}, Config: Config{MaxAttempts: 3}}
	raw := []byte(`{"update_id":1,"business_message":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"from":{"id":10},"text":"legacy"}}`)
	var update Update
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(context.Background(), db, update, raw); err != nil {
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
		t.Fatalf("legacy update generated traffic: %s %d", job.Status, sends.Load())
	}
}

func TestGroupThrottleDoesNotDelayCustomerReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`) }))
	defer server.Close()
	api := &Telegram{Token: "123:test", Base: server.URL, Client: server.Client(), SendInterval: 5 * time.Second, PrivateSendInterval: time.Second}
	if _, err := api.Send(context.Background(), &Message{Text: "inbound mirror"}, -10099, 88); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := api.Send(ctx, &Message{Text: "customer reply"}, 10, 0); err != nil {
		t.Fatal("customer reply waited for the group's cooldown", err)
	}
}

func TestDirectCustomerMediaAndTopicIsolation(t *testing.T) {
	db := integrationDB(t)
	type delivery struct {
		method       string
		chat, thread int64
	}
	var mu sync.Mutex
	var delivered []delivery
	topics := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var params map[string]any
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Error(err)
		}
		if params["business_connection_id"] != nil {
			t.Error("ordinary send included Business credentials")
		}
		if method == "createForumTopic" {
			topics++
			thread := 88
			if strings.HasSuffix(params["name"].(string), "20") {
				thread = 99
			}
			fmt.Fprintf(w, `{"ok":true,"result":{"message_thread_id":%d}}`, thread)
			return
		}
		thread := int64(0)
		if n, ok := params["message_thread_id"].(float64); ok {
			thread = int64(n)
		}
		delivered = append(delivered, delivery{method, int64(params["chat_id"].(float64)), thread})
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer server.Close()
	worker := &Worker{DB: db, API: &Telegram{Token: "123:test", Base: server.URL, Client: server.Client()}, Config: Config{GroupID: -10099, Agents: map[int64]bool{2: true}, MaxAttempts: 3}}
	next := int64(0)
	process := func(m Message) {
		t.Helper()
		next++
		m.ID = next
		m.Date = time.Now().Unix()
		u := Update{ID: next, Message: &m}
		raw, _ := json.Marshal(u)
		if err := Enqueue(context.Background(), db, u, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := worker.Step(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		var job Job
		if err := db.First(&job, "update_id=?", next).Error; err != nil {
			t.Fatal(err)
		}
		if job.Status != "done" {
			t.Fatalf("%s: %s", job.Status, job.LastError)
		}
	}
	media := []struct {
		message Message
		method  string
	}{
		{Message{Text: "/start"}, "sendMessage"},
		{Message{Photo: []Media{{FileID: "photo"}}, Caption: "照片"}, "sendPhoto"},
		{Message{Video: &Media{FileID: "video"}}, "sendVideo"},
		{Message{Voice: &Media{FileID: "voice"}}, "sendVoice"},
		{Message{Document: &Media{FileID: "document"}}, "sendDocument"},
	}
	for _, item := range media {
		incoming := item.message
		incoming.Chat = Chat{ID: 10, Type: "private"}
		incoming.From = &User{ID: 10}
		process(incoming)
		reply := item.message
		reply.Chat = Chat{ID: -10099, Type: "supergroup"}
		reply.From = &User{ID: 2}
		reply.ThreadID = 88
		process(reply)
	}
	process(Message{Chat: Chat{ID: 20, Type: "private"}, From: &User{ID: 20}, Text: "second customer"})
	process(Message{Chat: Chat{ID: -10099, Type: "supergroup"}, From: &User{ID: 2}, ThreadID: 99, Text: "second reply"})
	mu.Lock()
	defer mu.Unlock()
	if topics != 2 || len(delivered) != 12 {
		t.Fatalf("topics=%d deliveries=%d", topics, len(delivered))
	}
	for i, item := range media {
		if delivered[2*i] != (delivery{item.method, -10099, 88}) || delivered[2*i+1] != (delivery{item.method, 10, 0}) {
			t.Fatalf("wrong media route: %+v", delivered)
		}
	}
	if delivered[10] != (delivery{"sendMessage", -10099, 99}) || delivered[11] != (delivery{"sendMessage", 20, 0}) {
		t.Fatal("second customer's reply crossed routes", delivered)
	}
}

func TestBusinessDatabaseUpgradePreservesAndIsolatesHistory(t *testing.T) {
	db := integrationDB(t)
	if err := db.Exec("DROP TABLE bot_routes").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version>=3").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO routes(business_id,customer_id,group_id,thread_id,last_incoming) VALUES ('old',10,-10099,55,now())").Error; err != nil {
		t.Fatal(err)
	}
	jobs := []Job{{UpdateID: 1, SourceKey: "legacy-pending", Payload: `{}`, Status: "pending", NextAt: time.Now()}, {UpdateID: 2, SourceKey: "legacy-done", Payload: `{}`, Status: "done", NextAt: time.Now()}}
	if err := db.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal("upgrade not idempotent", err)
	}
	var legacyCount, newCount int64
	if err := db.Table("routes").Count(&legacyCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&Route{}).Count(&newCount).Error; err != nil {
		t.Fatal(err)
	}
	if legacyCount != 1 || newCount != 0 {
		t.Fatalf("legacy contacts became bot routes: old=%d new=%d", legacyCount, newCount)
	}
	var pending, done Job
	db.First(&pending, "update_id=1")
	db.First(&done, "update_id=2")
	if pending.Status != "dead" || !strings.Contains(pending.LastError, "Business mode retired") || done.Status != "done" {
		t.Fatal("legacy job audit history lost")
	}
	worker := &Worker{DB: db, Config: Config{GroupID: -10099, Agents: map[int64]bool{2: true}, MaxAttempts: 3}}
	u := Update{ID: 3, Message: &Message{ID: 3, ThreadID: 55, Chat: Chat{ID: -10099, Type: "supergroup"}, From: &User{ID: 2}, Text: "must not go to A's old contact"}}
	raw, _ := json.Marshal(u)
	if err := Enqueue(context.Background(), db, u, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Step(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var reply Job
	db.First(&reply, "update_id=3")
	if reply.Status != "ignored" {
		t.Fatal("old topic was reused as a bot route")
	}
}

func TestCheckAcceptsOrdinaryBotAndWebhookOnlySubscribesMessages(t *testing.T) {
	var webhook map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot123:test/getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":123,"is_bot":true,"can_connect_to_business":false}}`)
		case "/bot123:test/getChat":
			fmt.Fprint(w, `{"ok":true,"result":{"type":"supergroup","is_forum":true}}`)
		case "/bot123:test/getChatMember":
			fmt.Fprint(w, `{"ok":true,"result":{"status":"administrator","can_manage_topics":true}}`)
		case "/bot123:test/setWebhook":
			if err := json.NewDecoder(r.Body).Decode(&webhook); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			t.Error("unexpected Business API request", r.URL.Path)
			fmt.Fprint(w, `{"ok":false,"error_code":400}`)
		}
	}))
	defer server.Close()
	api := &Telegram{Token: "123:test", Base: server.URL, Client: server.Client()}
	if id, err := api.Check(context.Background(), -10099); err != nil || id != 123 {
		t.Fatalf("ordinary bot rejected: %d %v", id, err)
	}
	if err := RegisterWebhook(Config{WebhookURL: "https://example.invalid/webhook", WebhookSecret: "secret"}, api, context.Background()); err != nil {
		t.Fatal(err)
	}
	updates, ok := webhook["allowed_updates"].([]any)
	if !ok || len(updates) != 1 || updates[0] != "message" || webhook["drop_pending_updates"] != false || webhook["secret_token"] != "secret" {
		t.Fatalf("wrong webhook registration: %+v", webhook)
	}
}
