package dingtalk

import (
	"fmt"
	"net/url"
	"strings"
)

// Connection 包含一个钉钉自定义机器人的凭据。
// database 包会在持久化前加密这些字段。
type Connection struct {
	ID     int64
	Name   string
	URL    string
	Secret string
}

func (c Connection) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("DingTalk connection name is required")
	}
	if strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("DingTalk connection %q webhook URL is required", c.Name)
	}
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("DingTalk connection %q has invalid webhook URL", c.Name)
	}
	return nil
}
