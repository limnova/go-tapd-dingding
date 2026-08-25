// dingtalk 通过钉钉自定义机器人 Webhook 发送消息。
package dingtalk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"tapd-dingding/internal/config"
)

// Client 发送带签名或不带签名的钉钉 Webhook 请求。
type Client struct {
	cfg        config.WebhookConfig
	httpClient *http.Client
}

// Message 是钉钉文本或 Markdown 消息。
type Message struct {
	MsgType  string    `json:"msgtype"`
	Text     *Text     `json:"text,omitempty"`
	Markdown *Markdown `json:"markdown,omitempty"`
	At       At        `json:"at"`
}

// Text 是文本消息的内容。
type Text struct {
	Content string `json:"content"`
}

// Markdown 是 Markdown 消息的内容。
type Markdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// At 标识需要被 @ 的钉钉用户或手机号。
type At struct {
	Mobiles []string `json:"atMobiles,omitempty"`
	UserIDs []string `json:"atUserIds,omitempty"`
	IsAtAll bool     `json:"isAtAll"`
}

// NewClient 根据运行时 Webhook 配置创建客户端。
func NewClient(cfg config.WebhookConfig) *Client {
	return &Client{cfg: cfg, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

// Send 发送一条消息，并返回钉钉接口或网络传输错误。
func (c *Client) Send(ctx context.Context, message Message) error {
	if c == nil || c.httpClient == nil {
		return fmt.Errorf("DingTalk client is not initialized")
	}
	if message.MsgType == "" {
		message.MsgType = c.cfg.MessageType
	}
	if message.MsgType != "text" && message.MsgType != "markdown" {
		return fmt.Errorf("unsupported DingTalk message type %q", message.MsgType)
	}
	if message.MsgType == "text" && message.Text == nil {
		return fmt.Errorf("DingTalk text message content is required")
	}
	if message.MsgType == "markdown" && message.Markdown == nil {
		return fmt.Errorf("DingTalk markdown message content is required")
	}
	endpoint, err := url.Parse(c.cfg.URL)
	if err != nil {
		return fmt.Errorf("parse DingTalk webhook: %w", err)
	}
	if endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return fmt.Errorf("parse DingTalk webhook: URL must use http or https")
	}
	if c.cfg.Secret != "" {
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(c.cfg.Secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + c.cfg.Secret))
		query := endpoint.Query()
		query.Set("timestamp", timestamp)
		query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		endpoint.RawQuery = query.Encode()
	}
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode DingTalk message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create DingTalk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send DingTalk message: request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read DingTalk response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("DingTalk returned HTTP %d: %s", resp.StatusCode, truncate(string(responseBody), 500))
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return fmt.Errorf("decode DingTalk response: empty response body")
	}
	var result struct {
		ErrCode json.RawMessage `json:"errcode"`
		ErrMsg  string          `json:"errmsg"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return fmt.Errorf("decode DingTalk response: %w", err)
	}
	code := strings.Trim(string(result.ErrCode), `" `)
	if code != "" && code != "0" {
		return fmt.Errorf("DingTalk error %s: %s", code, result.ErrMsg)
	}
	return nil
}
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	const suffix = "..."
	if n <= len(suffix) {
		return validUTF8Prefix([]byte(s), n)
	}
	return validUTF8Prefix([]byte(s), n-len(suffix)) + suffix
}

func validUTF8Prefix(value []byte, maxBytes int) string {
	if maxBytes > len(value) {
		maxBytes = len(value)
	}
	for maxBytes > 0 && !utf8.Valid(value[:maxBytes]) {
		maxBytes--
	}
	return string(value[:maxBytes])
}
