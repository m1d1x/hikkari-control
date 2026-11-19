// Package notify tells the owner what the hub sees go wrong before a user
// says it (roadmap, decision 18.11): a message from the hub's Telegram bot to
// the owner's chat. The bot's token and the chat come from the hub's secrets
// (deploy/hub.env.enc): HUB_TELEGRAM_BOT_TOKEN and HUB_TELEGRAM_CHAT_ID.
// Without both nothing is sent and the hub says so once at start.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// telegramAPI is the Bot API's base; tests put their own server there.
const telegramAPI = "https://api.telegram.org"

// sendTimeout bounds one message: the watch that sends it waits for it.
const sendTimeout = 10 * time.Second

// maxText is Telegram's limit on a message's text, in characters.
const maxText = 4096

// Telegram sends the owner's messages through a bot.
type Telegram struct {
	token  string
	chat   string
	base   string
	client *http.Client
}

// TelegramFromEnv is the bot of HUB_TELEGRAM_BOT_TOKEN and
// HUB_TELEGRAM_CHAT_ID; nil when either is unset.
func TelegramFromEnv() *Telegram {
	token := strings.TrimSpace(os.Getenv("HUB_TELEGRAM_BOT_TOKEN"))
	chat := strings.TrimSpace(os.Getenv("HUB_TELEGRAM_CHAT_ID"))
	if token == "" || chat == "" {
		return nil
	}
	return NewTelegram(token, chat, telegramAPI)
}

// NewTelegram is the bot with token writing to chat through the Bot API at
// base.
func NewTelegram(token, chat, base string) *Telegram {
	return &Telegram{token: token, chat: chat, base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: sendTimeout}}
}

// Notify sends text to the owner's chat.
func (t *Telegram) Notify(ctx context.Context, text string) error {
	if r := []rune(text); len(r) > maxText {
		text = string(r[:maxText-1]) + "…"
	}
	form := url.Values{"chat_id": {t.chat}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		// The error names the URL, and the URL holds the token.
		return fmt.Errorf("telegram: the request did not go: %s", strings.ReplaceAll(err.Error(), t.token, "<token>"))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var answer struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &answer)
	if resp.StatusCode != http.StatusOK || !answer.OK {
		return fmt.Errorf("telegram: %d %s", resp.StatusCode, answer.Description)
	}
	return nil
}
