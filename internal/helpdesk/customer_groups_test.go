package helpdesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCustomerGroupBoundary(t *testing.T) {
	w := &Worker{Config: Config{GroupID: -99}}
	base := Message{ID: 1, Date: 1, Chat: Chat{ID: -10, Type: "supergroup"}, From: &User{ID: 20}, Text: "hello"}
	if !w.customerMessage(&base) {
		t.Fatal("allowed group rejected")
	}
	for _, change := range []func(*Message){
		func(m *Message) { m.Chat.ID = -99 },
		func(m *Message) { m.Chat.Type = "channel" },
		func(m *Message) { m.From.IsBot = true },
		func(m *Message) { m.From = nil },
		func(m *Message) { m.SenderChat = &Chat{ID: -10} },
		func(m *Message) { m.IsService = true },
		func(m *Message) { m.ThreadID = -1 },
		func(m *Message) { m.BusinessID = "legacy" },
	} {
		m := base
		user := *base.From
		m.From = &user
		change(&m)
		if w.customerMessage(&m) {
			t.Fatalf("invalid group message accepted: %+v", m)
		}
	}
}

func TestCustomerGroupRouting(t *testing.T) {
	db := integrationDB(t)
	type delivery struct {
		method       string
		chat, thread int64
	}
	var deliveries []delivery
	topics := int64(100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/bot123:test/createForumTopic" {
			topics++
			fmt.Fprintf(w, `{"ok":true,"result":{"message_thread_id":%d}}`, topics)
			return
		}
		thread := int64(0)
		if value, ok := p["message_thread_id"].(float64); ok {
			thread = int64(value)
		}
		deliveries = append(deliveries, delivery{r.URL.Path, int64(p["chat_id"].(float64)), thread})
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer server.Close()
	worker := &Worker{DB: db, API: &Telegram{Token: "123:test", Base: server.URL, Client: server.Client()}, Config: Config{GroupID: -99, Agents: map[int64]bool{2: true}, MaxAttempts: 3}}
	next := int64(0)
	process := func(m Message, want string) {
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
		db.First(&job, "update_id=?", next)
		if job.Status != want {
			t.Fatalf("want %s got %s: %s", want, job.Status, job.LastError)
		}
	}
	media := []Message{{Text: "hello"}, {Photo: []Media{{FileID: "photo"}}}, {Video: &Media{FileID: "video"}}, {Voice: &Media{FileID: "voice"}}, {Document: &Media{FileID: "file"}}}
	for _, m := range media {
		m.Chat = Chat{ID: -10, Type: "group"}
		m.From = &User{ID: 20}
		process(m, "done")
		m.Chat = Chat{ID: -99, Type: "supergroup"}
		m.ThreadID = 101
		m.From = &User{ID: 2}
		process(m, "done")
	}
	// A different group and two forum topics must never share a return address.
	for _, thread := range []int64{0, 7, 8} {
		process(Message{Chat: Chat{ID: -20, Type: "supergroup"}, ThreadID: thread, IsTopicMessage: thread > 0, From: &User{ID: 20}, Text: "forum"}, "done")
		process(Message{Chat: Chat{ID: -99, Type: "supergroup"}, ThreadID: topics, From: &User{ID: 2}, Text: "reply"}, "done")
		if got := deliveries[len(deliveries)-1]; got.chat != -20 || got.thread != thread {
			t.Fatalf("crossed forum route: %+v", got)
		}
	}
	if topics != 104 {
		t.Fatalf("Topic reuse broken: %d", topics)
	}
	for i := range media {
		if deliveries[2*i].chat != -99 || deliveries[2*i].thread != 101 || deliveries[2*i+1].chat != -10 || deliveries[2*i+1].thread != 0 {
			t.Fatal("crossed group route")
		}
	}
	before := len(deliveries)
	process(Message{Chat: Chat{ID: -99, Type: "supergroup"}, ThreadID: 101, From: &User{ID: 3}, Text: "unauthorized"}, "ignored")
	process(Message{Chat: Chat{ID: -99, Type: "supergroup"}, ThreadID: 101, From: &User{ID: 2}, SenderChat: &Chat{ID: -99}, Text: "anonymous"}, "ignored")
	process(Message{Chat: Chat{ID: -10, Type: "group"}, From: &User{ID: 123, IsBot: true}, Text: "echo"}, "ignored")
	if len(deliveries) != before {
		t.Fatal("ignored message sent")
	}
}

func TestGroupMigrationPreservesPrivateRoutesAndQueue(t *testing.T) {
	db := integrationDB(t)
	if err := db.Exec("DROP INDEX bot_routes_customer_thread; ALTER TABLE bot_routes DROP COLUMN customer_thread_id; ALTER TABLE bot_routes ADD CONSTRAINT bot_routes_customer_id_key UNIQUE(customer_id); DELETE FROM schema_migrations WHERE version=4").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO bot_routes(customer_id,group_id,thread_id,last_incoming) VALUES (10,-99,88,now())").Error; err != nil {
		t.Fatal(err)
	}
	job := Job{UpdateID: 1, SourceKey: "p:10:1", Payload: `{"update_id":1}`, Status: "pending", NextAt: time.Now()}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var route Route
	if err := db.First(&route, "customer_id=10").Error; err != nil {
		t.Fatal(err)
	}
	if route.ThreadID != 88 || route.CustomerThreadID != 0 {
		t.Fatalf("private route changed: %+v", route)
	}
	if err := db.First(&job, "update_id=1").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "pending" {
		t.Fatal("existing queue changed")
	}
	// Same group may have two independent source Topics under the new unique key.
	for _, thread := range []int64{7, 8} {
		r := Route{CustomerID: -10, CustomerThreadID: thread, GroupID: -99, ThreadID: 100 + thread, LastIncoming: time.Now()}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCustomerSourceThread(t *testing.T) {
	for _, tt := range []struct {
		chat  int64
		topic bool
		want  int64
	}{{-10, false, 0}, {-10, true, 7}, {10, true, 0}} {
		m := Message{Chat: Chat{ID: tt.chat}, ThreadID: 7, IsTopicMessage: tt.topic}
		if got := customerThread(&m); got != tt.want {
			t.Fatalf("source thread=%d want=%d", got, tt.want)
		}
	}
}
