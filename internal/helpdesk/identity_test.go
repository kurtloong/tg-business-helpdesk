package helpdesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIdentityCard(t *testing.T) {
	m := &Message{From: &User{ID: 123, FirstName: "张三", LastName: "Lee", Username: "zhangsan"}, Chat: Chat{ID: -10, Type: "supergroup", Title: "客户交流"}, ThreadID: 7, IsTopicMessage: true}
	card := staffIdentity(m)
	if card.Rows[0][0].Text != "👤 张三 Lee · @zhangsan" || card.Rows[0][0].URL != "https://t.me/zhangsan" || card.Rows[0][0].Copy != nil {
		t.Fatalf("bad sender: %+v", card)
	}
	if card.Rows[1][0].Copy.Text != "123" || card.Rows[2][0].Copy.Text != "-10" || !strings.Contains(card.Rows[2][0].Text, "客户交流") || card.Rows[3][0].Copy.Text != "7" {
		t.Fatal("missing source identifiers")
	}
	m.From = &User{ID: 456, FirstName: "张三"}
	m.Chat = Chat{ID: 456, Type: "private"}
	card = staffIdentity(m)
	if len(card.Rows) != 2 || card.Rows[0][0].URL != "" || card.Rows[0][0].Copy.Text != "456" {
		t.Fatal("user without username lost identity")
	}
	m.From = &User{ID: 789, Username: "evil.example/path"}
	card = staffIdentity(m)
	if card.Rows[0][0].Text != "👤 未提供姓名" || card.Rows[0][0].URL != "" {
		t.Fatal("invalid username became link")
	}
	if got := identityLabel("a\n\u202eb\t c", 20); got != "a b c" {
		t.Fatalf("unsafe label: %q", got)
	}
	if got := identityLabel(strings.Repeat("🙂", 100), 48); len([]rune(got)) != 48 || !strings.HasSuffix(got, "…") {
		t.Fatal("unicode truncation failed")
	}
}

func TestIdentityPreservesFullPayload(t *testing.T) {
	for _, m := range []Message{
		{Text: strings.Repeat("🙂", 4096), Entities: json.RawMessage(`[{"type":"bold","offset":0,"length":2}]`)},
		{Photo: []Media{{FileID: "photo"}}, Caption: strings.Repeat("字", 1024), CaptionEntities: json.RawMessage(`[{"type":"bold","offset":0,"length":1}]`)},
		{Video: &Media{FileID: "video"}}, {Voice: &Media{FileID: "voice"}}, {Document: &Media{FileID: "file"}},
	} {
		_, before, err := payload(&m)
		if err != nil {
			t.Fatal(err)
		}
		m.From = &User{ID: 10, FirstName: "张三"}
		m.Chat = Chat{ID: 10, Type: "private"}
		m.StaffIdentity = staffIdentity(&m)
		_, after, err := payload(&m)
		if err != nil {
			t.Fatal(err)
		}
		if after["reply_markup"] == nil {
			t.Fatal("media missing identity")
		}
		delete(after, "reply_markup")
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		if string(a) != string(b) {
			t.Fatal("identity changed text, caption or entity offsets")
		}
	}
	// Client-controlled markup must not be decoded into the staff identity.
	var m Message
	if err := json.Unmarshal([]byte(`{"text":"hi","reply_markup":{"inline_keyboard":[[{"text":"spoof"}]]},"StaffIdentity":{"inline_keyboard":[]}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.StaffIdentity != nil {
		t.Fatal("incoming identity trusted")
	}
}

func TestIdentityOnlySentToStaff(t *testing.T) {
	db := integrationDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if strings.HasSuffix(r.URL.Path, "createForumTopic") {
			fmt.Fprint(w, `{"ok":true,"result":{"message_thread_id":88}}`)
			return
		}
		chat := int64(p["chat_id"].(float64))
		if chat == -99 {
			data, _ := json.Marshal(p["reply_markup"])
			if !strings.Contains(string(data), "张三") || !strings.Contains(string(data), "客户交流群") || !strings.Contains(string(data), "用户 ID：20") {
				t.Errorf("missing staff identity: %s", data)
			}
		} else if p["reply_markup"] != nil {
			t.Error("identity leaked into customer reply")
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer server.Close()
	worker := &Worker{DB: db, API: &Telegram{Token: "123:test", Base: server.URL, Client: server.Client()}, Config: Config{GroupID: -99, Agents: map[int64]bool{2: true}, MaxAttempts: 3}}
	process := func(id int64, m Message) {
		t.Helper()
		m.ID = id
		m.Date = time.Now().Unix()
		u := Update{ID: id, Message: &m}
		raw, _ := json.Marshal(u)
		if err := Enqueue(context.Background(), db, u, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := worker.Step(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		var job Job
		if err := db.First(&job, "update_id=?", id).Error; err != nil || job.Status != "done" {
			t.Fatalf("%+v %v", job, err)
		}
	}
	process(1, Message{Chat: Chat{ID: -10, Type: "group", Title: "客户交流群"}, From: &User{ID: 20, FirstName: "张三", Username: "zhangsan"}, Text: "hello"})
	process(2, Message{Chat: Chat{ID: -99, Type: "supergroup"}, ThreadID: 88, From: &User{ID: 2, FirstName: "客服"}, Text: "reply"})
	// Unsupported types retain the sender card alongside the existing fallback text.
	process(3, Message{Chat: Chat{ID: -10, Type: "group", Title: "客户交流群"}, From: &User{ID: 20, FirstName: "张三"}})
}
