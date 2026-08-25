package dingtalk

import (
	"fmt"
	"net/url"
	"strings"
)

// Connection contains the credentials for one DingTalk custom robot. The
// database package encrypts this value before persisting it.
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
