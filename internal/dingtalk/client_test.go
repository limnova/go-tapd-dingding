package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tapd-dingding/internal/config"
)

func TestSendSignsAndSendsMention(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("timestamp") == "" || r.URL.Query().Get("sign") == "" {
			t.Error("signature query is missing")
		}
		var msg Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatal(err)
		}
		if len(msg.At.UserIDs) != 1 || msg.At.UserIDs[0] != "u1" || !strings.Contains(msg.Markdown.Text, "@张三") {
			t.Fatalf("unexpected message: %+v", msg)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()
	client := NewClient(config.WebhookConfig{URL: server.URL, Secret: "secret", MessageType: "markdown"})
	err := client.Send(context.Background(), Message{MsgType: "markdown", Markdown: &Markdown{Title: "title", Text: "@张三"}, At: At{UserIDs: []string{"u1"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSendPreservesTransportError(t *testing.T) {
	transportErr := errors.New("transport unavailable")
	client := NewClient(config.WebhookConfig{URL: "https://example.test/robot", MessageType: "text"})
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})
	err := client.Send(context.Background(), Message{MsgType: "text", Text: &Text{Content: "message"}})
	if !errors.Is(err, transportErr) {
		t.Fatalf("transport error was not wrapped: %v", err)
	}
}

func TestConnectionValidateRejectsNonHTTPWebhook(t *testing.T) {
	if err := (Connection{Name: "robot", URL: "ftp://example.test/webhook"}).Validate(); err == nil {
		t.Fatal("non-HTTP webhook URL was accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
