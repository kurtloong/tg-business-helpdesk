package helpdesk

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// These buttons are generated locally; incoming keyboards never cross the relay.
type InlineKeyboard struct {
	Rows [][]IdentityButton `json:"inline_keyboard"`
}
type IdentityButton struct {
	Text string    `json:"text"`
	URL  string    `json:"url,omitempty"`
	Copy *CopyText `json:"copy_text,omitempty"`
}
type CopyText struct {
	Text string `json:"text"`
}

// Remove line breaks and invisible direction overrides from customer-controlled labels.
func identityLabel(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	runes := []rune(strings.Join(strings.Fields(s), " "))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}

var telegramUsername = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

func staffIdentity(m *Message) *InlineKeyboard {
	id := strconv.FormatInt(m.From.ID, 10)
	name := identityLabel(m.From.FirstName+" "+m.From.LastName, 48)
	if name == "" {
		name = "未提供姓名"
	}
	sender := IdentityButton{Text: "👤 " + name, Copy: &CopyText{Text: id}}
	if telegramUsername.MatchString(m.From.Username) {
		sender.Text += " · @" + m.From.Username
		sender.URL = "https://t.me/" + m.From.Username
		sender.Copy = nil
	}
	keyboard := &InlineKeyboard{Rows: [][]IdentityButton{
		{sender},
		{{Text: "用户 ID：" + id, Copy: &CopyText{Text: id}}},
	}}
	if m.Chat.ID < 0 {
		title := identityLabel(m.Chat.Title, 40)
		if title == "" {
			title = "未提供群名"
		}
		chatID := strconv.FormatInt(m.Chat.ID, 10)
		keyboard.Rows = append(keyboard.Rows, []IdentityButton{{Text: "群：" + title + " · " + chatID, Copy: &CopyText{Text: chatID}}})
		if thread := customerThread(m); thread > 0 {
			keyboard.Rows = append(keyboard.Rows, []IdentityButton{{Text: fmt.Sprintf("原群话题 ID：%d", thread), Copy: &CopyText{Text: strconv.FormatInt(thread, 10)}}})
		}
	}
	return keyboard
}
