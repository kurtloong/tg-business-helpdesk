package helpdesk

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Token, DSN, WebhookSecret, AdminToken, APIBase, Listen, WebhookURL string
	GroupID                                                            int64
	Agents                                                             map[int64]bool
	MaxAttempts                                                        int
	PollInterval                                                       time.Duration
}

func ids(s string) (map[int64]bool, error) {
	result := map[int64]bool{}
	for _, v := range strings.Split(s, ",") {
		id, e := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if e != nil || id <= 0 {
			return nil, fmt.Errorf("IDs must be comma-separated positive integers")
		}
		result[id] = true
	}
	return result, nil
}
func LoadConfig() (Config, error) {
	c := Config{Token: os.Getenv("BOT_TOKEN"), DSN: os.Getenv("DATABASE_URL"), WebhookSecret: os.Getenv("WEBHOOK_SECRET"), AdminToken: os.Getenv("ADMIN_TOKEN"), APIBase: "https://api.telegram.org", Listen: ":8080", MaxAttempts: 8, PollInterval: 250 * time.Millisecond, WebhookURL: os.Getenv("WEBHOOK_URL")}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("TELEGRAM_API_BASE"); v != "" {
		c.APIBase = strings.TrimRight(v, "/")
	}
	var err error
	if c.GroupID, err = strconv.ParseInt(os.Getenv("SUPPORT_GROUP_ID"), 10, 64); err != nil || c.GroupID >= 0 {
		return c, fmt.Errorf("SUPPORT_GROUP_ID must be negative")
	}
	if c.Agents, err = ids(os.Getenv("AGENT_IDS")); err != nil {
		return c, fmt.Errorf("AGENT_IDS: %w", err)
	}
	if strings.Contains(c.Token, "REPLACE") || !regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`).MatchString(c.Token) {
		return c, fmt.Errorf("invalid BOT_TOKEN")
	}
	if c.DSN == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if strings.Contains(c.WebhookSecret, "REPLACE") || !regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`).MatchString(c.WebhookSecret) {
		return c, fmt.Errorf("WEBHOOK_SECRET requires 32-256 allowed characters")
	}
	if strings.Contains(c.AdminToken, "REPLACE") || len(c.AdminToken) < 32 || c.AdminToken == c.WebhookSecret {
		return c, fmt.Errorf("ADMIN_TOKEN requires at least 32 characters, different from webhook secret")
	}
	u, e := url.Parse(c.APIBase)
	if e != nil || u.Host == "" || u.Scheme != "https" {
		return c, fmt.Errorf("TELEGRAM_API_BASE must be HTTPS")
	}
	return c, nil
}
