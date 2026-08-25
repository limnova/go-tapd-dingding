package service

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"tapd-dingding/internal/config"
	"tapd-dingding/internal/dingtalk"
	"tapd-dingding/internal/tapd"
)

var markdownLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

func buildMessage(m config.Monitor, bug tapd.Bug) dingtalk.Message {
	at, mentionText := buildMentions(m, bug)
	lines := []string{
		fmt.Sprintf("**标题**：%s", escapeMarkdown(emptyAs(bug.Title, "无标题"))),
	}
	if bugURL := buildBugURL(m.BugURLTemplate, bug); bugURL != "" {
		lines = append(lines, "", fmt.Sprintf("**TAPD 链接**：[打开缺陷](%s)", bugURL))
	}
	lines = append(lines,
		"",
		fmt.Sprintf("**创建人**：%s", escapeMarkdown(emptyAs(bug.Reporter, "未知"))),
		"",
		fmt.Sprintf("**优先级**：%s", escapeMarkdown(emptyAs(first(bug.PriorityLabel, bug.Priority), "未知"))),
	)
	if m.Webhook.IncludeDesc && strings.TrimSpace(bug.Description) != "" {
		lines = append(lines, "", fmt.Sprintf("**描述**：%s", escapeMarkdown(bug.Description)))
	}
	if len(mentionText) > 0 {
		lines = append(lines, "", "提醒："+strings.Join(mentionText, " "))
	}

	text := limitMessage(strings.Join(lines, "\n"), m.Webhook.MaxBodyBytes)
	return makeMessage(m, text, strings.TrimSpace(m.TitlePrefix+" TAPD缺陷"), at)
}

func buildDailyReportMessage(m config.Monitor, bugs []tapd.Bug, reportAt time.Time) dingtalk.Message {
	at, mentionText := buildMentions(m, tapd.Bug{})
	lines := []string{
		"### " + strings.TrimSpace(m.TitlePrefix+" TAPD 缺陷日报"),
		"",
		fmt.Sprintf("**时间**：%s　**数量**：%d", reportAt.Format("2006-01-02 15:04"), len(bugs)),
	}
	if len(mentionText) > 0 {
		lines = append(lines, "", "提醒："+strings.Join(mentionText, " "))
	}
	if len(bugs) == 0 {
		lines = append(lines, "", "当前没有符合监控条件的缺陷。")
	} else {
		lines = append(lines, "")
		for _, bug := range bugs {
			item := fmt.Sprintf("**标题**：%s", escapeMarkdown(emptyAs(bug.Title, "无标题")))
			if bugURL := buildBugURL(m.BugURLTemplate, bug); bugURL != "" {
				item += fmt.Sprintf("\n\n**TAPD 链接**：[打开缺陷](%s)", bugURL)
			}
			item += fmt.Sprintf("\n\n**创建人**：%s\n\n**优先级**：%s", escapeMarkdown(emptyAs(bug.Reporter, "未知")), escapeMarkdown(emptyAs(first(bug.PriorityLabel, bug.Priority), "未知")))
			if m.Webhook.IncludeDesc && strings.TrimSpace(bug.Description) != "" {
				item += "\n\n**描述**：" + escapeMarkdown(bug.Description)
			}
			lines = append(lines, item, "")
		}
	}

	text := limitMessage(strings.Join(lines, "\n"), m.Webhook.MaxBodyBytes)
	return makeMessage(m, text, strings.TrimSpace(m.TitlePrefix+" TAPD缺陷日报"), at)
}

func makeMessage(m config.Monitor, text, title string, at dingtalk.At) dingtalk.Message {
	if m.Webhook.MessageType == "text" {
		return dingtalk.Message{MsgType: "text", Text: &dingtalk.Text{Content: stripMarkdown(text)}, At: at}
	}
	return dingtalk.Message{MsgType: "markdown", Markdown: &dingtalk.Markdown{Title: title, Text: text}, At: at}
}

func buildBugURL(template string, bug tapd.Bug) string {
	return strings.NewReplacer(
		"{workspace_id}", bug.WorkspaceID,
		"{id}", bug.ID,
	).Replace(template)
}

func buildMentions(m config.Monitor, bug tapd.Bug) (dingtalk.At, []string) {
	mentions := resolveMentions(m, bug)
	at := dingtalk.At{}
	mentionText := make([]string, 0, len(mentions))
	seenIDs, seenMobiles := map[string]bool{}, map[string]bool{}
	for _, recipient := range mentions {
		if recipient.UserID != "" && !seenIDs[recipient.UserID] {
			at.UserIDs = append(at.UserIDs, recipient.UserID)
			seenIDs[recipient.UserID] = true
			mention := recipient.UserID
			if recipient.Name != "" {
				mention += "(" + recipient.Name + ")"
			}
			mentionText = append(mentionText, "@"+mention)
		}
		if recipient.Mobile != "" && !seenMobiles[recipient.Mobile] {
			at.Mobiles = append(at.Mobiles, recipient.Mobile)
			seenMobiles[recipient.Mobile] = true
			if recipient.UserID == "" {
				mentionText = append(mentionText, "@"+recipient.Mobile)
			}
		}
	}
	return at, mentionText
}

func resolveMentions(m config.Monitor, bug tapd.Bug) []config.Recipient {
	values := map[string]string{
		"current_owner": bug.CurrentOwner,
		"reporter":      bug.Reporter,
		"de":            bug.De,
		"fixer":         bug.Fixer,
		"te":            bug.Te,
		"confirmer":     bug.Confirmer,
		"cc":            bug.Cc,
		"participator":  bug.Participator,
	}
	wanted := make(map[string]bool)
	for _, name := range m.DefaultRecipients {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}
	for _, field := range m.MentionFields {
		for _, account := range splitAccounts(values[field]) {
			wanted[account] = true
		}
	}

	var result []config.Recipient
	for _, recipient := range m.Recipients {
		match := wanted[recipient.Name]
		for _, account := range recipient.TAPDAccounts {
			if wanted[account] {
				match = true
				break
			}
		}
		if match {
			result = append(result, recipient)
		}
	}
	return result
}

func splitAccounts(value string) []string {
	var accounts []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '|' || r == ',' || r == ';' || unicode.IsSpace(r)
	}) {
		if item = strings.TrimSpace(item); item != "" {
			accounts = append(accounts, item)
		}
	}
	return accounts
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func escapeMarkdown(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	return strings.NewReplacer(
		"\\", "\\\\",
		"*", "\\*",
		"_", "\\_",
		"`", "\\`",
		"[", "\\[",
		"]", "\\]",
	).Replace(value)
}

func limitMessage(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return value
	}
	return truncateBytes(value, maxBytes)
}

func truncateBytes(value string, maxBytes int) string {
	valueBytes := []byte(value)
	if len(valueBytes) <= maxBytes {
		return value
	}
	const suffix = "..."
	if maxBytes <= len(suffix) {
		return validUTF8Prefix(valueBytes, maxBytes)
	}
	prefixLength := maxBytes - len(suffix)
	return validUTF8Prefix(valueBytes, prefixLength) + suffix
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

func stripMarkdown(value string) string {
	value = markdownLinkPattern.ReplaceAllString(value, "$1")
	value = html.UnescapeString(value)
	return strings.NewReplacer("**", "", "### ", "", "\\*", "*", "\\_", "_", "\\`", "`").Replace(value)
}
