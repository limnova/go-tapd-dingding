package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"tapd-dingding/internal/config"
	"tapd-dingding/internal/dingtalk"
	"tapd-dingding/internal/tapd"
)

func TestBuildMessageLayout(t *testing.T) {
	message := buildMessage(config.Monitor{
		TitlePrefix:    "[研发]",
		BugURLTemplate: "https://www.tapd.cn/{workspace_id}/bugtrace/bugs/view?bug_id={id}",
		Webhook: config.WebhookConfig{
			MessageType: "markdown",
			IncludeDesc: true,
		},
	}, tapd.Bug{
		ID: "1148649951001051762", Title: "消息中心模板文案问题", Status: "new",
		PriorityLabel: "中", Severity: "normal", CurrentOwner: "wangtt", Reporter: "lej",
		WorkspaceID: "48649951", Description: "第一行\n第二行",
	})

	if message.Markdown == nil {
		t.Fatal("expected markdown message")
	}
	for _, want := range []string{"**标题**：消息中心模板文案问题", "TAPD 链接", "**创建人**：lej", "**优先级**：中"} {
		if !strings.Contains(message.Markdown.Text, want) {
			t.Fatalf("message does not contain %q:\n%s", want, message.Markdown.Text)
		}
	}
	if !strings.Contains(message.Markdown.Text, "**描述**：第一行\n第二行") {
		t.Fatalf("message does not contain description:\n%s", message.Markdown.Text)
	}
	if strings.Contains(message.Markdown.Text, "###") {
		t.Fatalf("message should not contain a large heading:\n%s", message.Markdown.Text)
	}
	t.Log("rendered markdown:\n" + message.Markdown.Text)
}

func TestBuildMessageEscapesMarkdownAndSupportsText(t *testing.T) {
	monitor := config.Monitor{
		BugURLTemplate: "https://example.test/{id}",
		Webhook:        config.WebhookConfig{MessageType: "markdown"},
	}
	markdownMessage := buildMessage(monitor, tapd.Bug{ID: "1", Title: "*unsafe*", Reporter: "[reporter]"})
	if markdownMessage.Markdown == nil || !strings.Contains(markdownMessage.Markdown.Text, `\*unsafe\*`) {
		t.Fatalf("markdown escaping was lost: %+v", markdownMessage.Markdown)
	}

	monitor.Webhook.MessageType = "text"
	message := buildMessage(monitor, tapd.Bug{ID: "1", Title: "*unsafe*", Reporter: "[reporter]"})
	if message.Text == nil {
		t.Fatal("expected text message")
	}
	if strings.Contains(message.Text.Content, "[reporter]") {
		t.Fatalf("markdown link syntax was not stripped: %q", message.Text.Content)
	}
	if !strings.Contains(message.Text.Content, "*unsafe*") {
		t.Fatalf("text message lost the title: %q", message.Text.Content)
	}
}

func TestTruncateBytesPreservesUTF8(t *testing.T) {
	for maxBytes := 1; maxBytes <= 12; maxBytes++ {
		value := truncateBytes("中文消息内容", maxBytes)
		if len([]byte(value)) > maxBytes {
			t.Fatalf("truncated value exceeds limit %d: %q", maxBytes, value)
		}
		if !utf8.ValidString(value) {
			t.Fatalf("truncated value is not valid UTF-8: %q", value)
		}
	}
}

func TestResolveMentionsForMineScopeDefaultRecipient(t *testing.T) {
	monitor := config.Monitor{
		DefaultRecipients: []string{"李明"},
		Recipients:        []config.Recipient{{Name: "李明", TAPDAccounts: []string{"lim1"}, UserID: "ding-user-1"}},
	}
	mentions := resolveMentions(monitor, tapd.Bug{Reporter: "other-account"})
	if len(mentions) != 1 || mentions[0].UserID != "ding-user-1" {
		t.Fatalf("expected default recipient to be mentioned, got %+v", mentions)
	}
}

func TestDecodeJSONRequiresOneObject(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "valid", body: `{"value":"ok"}`, want: true},
		{name: "null", body: `null`},
		{name: "multiple values", body: `{"value":"ok"}{}`},
		{name: "unknown field", body: `{"extra":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(test.body))
			var target struct {
				Value string `json:"value"`
			}
			err := decodeJSON(recorder, request, &target)
			if (err == nil) != test.want {
				t.Fatalf("decodeJSON error = %v, want success %v", err, test.want)
			}
		})
	}
}

func TestHandlerHealthEndpoints(t *testing.T) {
	handler := (&Service{}).Handler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Fatalf("unexpected health response: %d %q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected readiness response: %d", recorder.Code)
	}
}

func TestDingTalkQueueRetriesRateLimitAndPreservesOrder(t *testing.T) {
	queue := newDingTalkQueue(2, time.Nanosecond, time.Millisecond, nil)
	var (
		mu       sync.Mutex
		messages []string
		calls    int
	)
	queue.send = func(_ context.Context, _ config.WebhookConfig, message dingtalk.Message) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errors.New("DingTalk error 660026: rate limited")
		}
		messages = append(messages, message.Text.Content)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go queue.Run(ctx)

	if err := queue.Send(ctx, config.WebhookConfig{MessageType: "text"}, dingtalk.Message{MsgType: "text", Text: &dingtalk.Text{Content: "first"}}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Send(ctx, config.WebhookConfig{MessageType: "text"}, dingtalk.Message{MsgType: "text", Text: &dingtalk.Text{Content: "second"}}); err != nil {
		t.Fatal(err)
	}
	if queue.RateLimitRetries() != 1 {
		t.Fatalf("expected one rate-limit retry, got %d", queue.RateLimitRetries())
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.EqualFold(strings.Join(messages, ","), "first,second") {
		t.Fatalf("queue order was not preserved: %v", messages)
	}
}
