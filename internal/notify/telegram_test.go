package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTheBotSendsTheTextToTheOwnersChat(t *testing.T) {
	var path, chat, text string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = r.ParseForm()
		chat, text = r.PostForm.Get("chat_id"), r.PostForm.Get("text")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	if err := NewTelegram("123:abc", "42", srv.URL).Notify(context.Background(), "звонок магистрали красный"); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:abc/sendMessage" || chat != "42" || text != "звонок магистрали красный" {
		t.Fatalf("path %q chat %q text %q", path, chat, text)
	}
}

func TestARefusedMessageIsAnErrorThatDoesNotNameTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Forbidden: bot was blocked by the user"}`))
	}))
	defer srv.Close()
	err := NewTelegram("123:secret", "42", srv.URL).Notify(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "blocked by the user") {
		t.Fatalf("err %v", err)
	}
	srv.Close()
	err = NewTelegram("123:secret", "42", srv.URL).Notify(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("a request that did not go names the token: %v", err)
	}
}

func TestALongTextIsCutToTelegramsLimit(t *testing.T) {
	var text string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		text = r.PostForm.Get("text")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	if err := NewTelegram("t", "1", srv.URL).Notify(context.Background(), strings.Repeat("я", 5000)); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(text); n != maxText {
		t.Fatalf("%d characters", n)
	}
}

func TestNoBotWithoutBothTokenAndChat(t *testing.T) {
	t.Setenv("HUB_TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("HUB_TELEGRAM_CHAT_ID", "")
	if TelegramFromEnv() != nil {
		t.Fatal("a bot without a chat")
	}
	t.Setenv("HUB_TELEGRAM_CHAT_ID", "42")
	if TelegramFromEnv() == nil {
		t.Fatal("no bot with both set")
	}
}
