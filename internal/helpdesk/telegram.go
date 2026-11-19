package helpdesk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type APIError struct {
	Code        int
	RetryAfter  int
	Description string
}

func (e *APIError) Error() string   { return fmt.Sprintf("Telegram %d: %s", e.Code, e.Description) }
func (e *APIError) Temporary() bool { return e.Code == 429 || e.Code >= 500 }

type Telegram struct {
	Token, Base         string
	Client              *http.Client
	SendInterval        time.Duration
	PrivateSendInterval time.Duration
	sendMu              sync.Mutex
	nextSend            time.Time
	nextPrivateSend     time.Time
}

func NewTelegram(c Config) *Telegram {
	return &Telegram{Token: c.Token, Base: c.APIBase, SendInterval: 3200 * time.Millisecond, PrivateSendInterval: time.Second, Client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (t *Telegram) Call(ctx context.Context, method string, p any, out any) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Base+"/bot"+t.Token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return errors.New("invalid Telegram request URL")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := t.Client.Do(req)
	if err != nil {
		return errors.New("Telegram transport failure (delivery may be ambiguous)")
	}
	defer res.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&envelope); err != nil {
		if res.StatusCode >= 400 {
			return &APIError{Code: res.StatusCode, Description: "non-JSON response"}
		}
		return errors.New("invalid Telegram response (delivery may be ambiguous)")
	}
	if !envelope.OK || res.StatusCode >= 400 {
		code := envelope.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &APIError{Code: code, Description: envelope.Description, RetryAfter: envelope.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

var ErrUnsupported = errors.New("unsupported message type")

func payload(m *Message) (string, map[string]any, error) {
	p := map[string]any{}
	method := ""
	switch {
	case len(m.Photo) > 0:
		method = "sendPhoto"
		p["photo"] = m.Photo[len(m.Photo)-1].FileID
	case m.Video != nil:
		method = "sendVideo"
		p["video"] = m.Video.FileID
	case m.Voice != nil:
		method = "sendVoice"
		p["voice"] = m.Voice.FileID
	case m.Document != nil:
		method = "sendDocument"
		p["document"] = m.Document.FileID
	case m.Text != "":
		method = "sendMessage"
		p["text"] = m.Text
		if len(m.Entities) > 0 {
			p["entities"] = m.Entities
		}
	default:
		return "", nil, ErrUnsupported
	}
	if method != "sendMessage" && m.Caption != "" {
		p["caption"] = m.Caption
		if len(m.CaptionEntities) > 0 {
			p["caption_entities"] = m.CaptionEntities
		}
	}
	if m.StaffIdentity != nil {
		p["reply_markup"] = m.StaffIdentity
	}
	return method, p, nil
}
func (t *Telegram) Send(ctx context.Context, m *Message, chatID, threadID int64) (int64, error) {
	method, p, err := payload(m)
	if err != nil {
		return 0, err
	}
	p["chat_id"] = chatID
	if threadID > 0 {
		p["message_thread_id"] = threadID
	}
	// A single active worker shares this limiter, including dead-letter alerts.
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	next, interval := &t.nextSend, t.SendInterval
	if chatID > 0 {
		next, interval = &t.nextPrivateSend, t.PrivateSendInterval
	}
	if delay := time.Until(*next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	*next = time.Now().Add(interval)
	var sent Message
	if err = t.Call(ctx, method, p, &sent); err != nil {
		return 0, err
	}
	// Telegram may return ID 0 for a successfully scheduled message in a large chat.
	// API success must not be retried solely because the final message ID is not yet available.
	return sent.ID, nil
}

// Check validates an ordinary bot and its private support forum; no Business capability is required.
func (t *Telegram) Check(ctx context.Context, groupID int64) (int64, error) {
	var me User
	if err := t.Call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return 0, err
	}
	if me.ID <= 0 || !me.IsBot {
		return 0, errors.New("getMe did not return a bot account")
	}
	var chat struct {
		Type            string   `json:"type"`
		Forum           bool     `json:"is_forum"`
		Username        string   `json:"username"`
		ActiveUsernames []string `json:"active_usernames"`
	}
	if err := t.Call(ctx, "getChat", map[string]any{"chat_id": groupID}, &chat); err != nil {
		return 0, err
	}
	if chat.Type != "supergroup" || !chat.Forum || chat.Username != "" || len(chat.ActiveUsernames) > 0 {
		return 0, errors.New("support group must be a private forum supergroup")
	}
	var member struct {
		Status string `json:"status"`
		Manage bool   `json:"can_manage_topics"`
	}
	if err := t.Call(ctx, "getChatMember", map[string]any{"chat_id": groupID, "user_id": me.ID}, &member); err != nil {
		return 0, err
	}
	if member.Status != "administrator" || !member.Manage {
		return 0, errors.New("bot requires administrator and can_manage_topics")
	}
	return me.ID, nil
}
