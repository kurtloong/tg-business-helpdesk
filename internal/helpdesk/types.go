package helpdesk

import (
	"encoding/json"
	"time"
)

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type Media struct {
	FileID string `json:"file_id"`
}
type Message struct {
	IsService         bool            `json:"-"`
	ID                int64           `json:"message_id"`
	ThreadID          int64           `json:"message_thread_id"`
	Date              int64           `json:"date"`
	BusinessID        string          `json:"business_connection_id"`
	From              *User           `json:"from"`
	SenderChat        *Chat           `json:"sender_chat"`
	SenderBusinessBot *User           `json:"sender_business_bot"`
	Chat              Chat            `json:"chat"`
	Text              string          `json:"text"`
	Entities          json.RawMessage `json:"entities"`
	Caption           string          `json:"caption"`
	CaptionEntities   json.RawMessage `json:"caption_entities"`
	Photo             []Media         `json:"photo"`
	Video             *Media          `json:"video"`
	Voice             *Media          `json:"voice"`
	Document          *Media          `json:"document"`
}

// Decode service markers without retaining extra client-controlled fields.
func (m *Message) UnmarshalJSON(data []byte) error {
	type plain Message
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = Message(value)
	for _, key := range []string{"forum_topic_created", "forum_topic_closed", "forum_topic_reopened", "forum_topic_edited", "general_forum_topic_hidden", "general_forum_topic_unhidden", "message_auto_delete_timer_changed", "chat_background_set", "pinned_message", "new_chat_members", "left_chat_member"} {
		if raw, ok := fields[key]; ok && string(raw) != "null" {
			m.IsService = true
			break
		}
	}
	return nil
}

type Update struct {
	ID      int64    `json:"update_id"`
	Message *Message `json:"message"`
}
type Job struct {
	UpdateID        int64     `gorm:"primaryKey;autoIncrement:false" json:"update_id"`
	SourceKey       string    `json:"source_key"`
	Payload         string    `gorm:"type:jsonb" json:"-"`
	Status          string    `json:"status"`
	Attempts        int       `json:"attempts"`
	NextAt          time.Time `json:"next_at"`
	LastError       string    `json:"last_error"`
	ResultMessageID int64     `json:"result_message_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type Route struct {
	ID           int64 `gorm:"primaryKey"`
	CustomerID   int64
	GroupID      int64
	ThreadID     int64
	LastIncoming time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Keep historical Business routes isolated; they are not proof of a bot conversation.
func (Route) TableName() string { return "bot_routes" }
