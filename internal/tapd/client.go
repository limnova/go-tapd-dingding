// Package tapd implements the TAPD MCP client used by the service.
package tapd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Connection contains the encrypted TAPD MCP credentials and query options.
type Connection struct {
	ID          int64
	Name        string
	AccessToken string
	Statuses    []string
	Fields      string
}

// DefaultMCPURL is the local TAPD Streamable HTTP MCP endpoint.
const DefaultMCPURL = "http://localhost:8000/mcp/"

// Client discovers TAPD MCP tools and lists bugs.
type Client struct {
	cfg        Connection
	httpClient *http.Client
	endpoint   string
}

// Bug is the normalized subset of TAPD fields used by notifications.
type Bug struct {
	ID, Title, Description, Priority, PriorityLabel, Severity, Module, Status string
	Reporter, CurrentOwner, De, Fixer, Te, Confirmer, Cc, Participator        string
	Created, Modified, WorkspaceID                                            string
}

func (c Connection) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("TAPD MCP connection name is required")
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return fmt.Errorf("TAPD MCP connection %q access_token is required", c.Name)
	}
	return nil
}

// NewClient creates a TAPD MCP client.
func NewClient(cfg Connection) *Client {
	return &Client{cfg: cfg, endpoint: DefaultMCPURL, httpClient: &http.Client{Timeout: 60 * time.Second}}
}

type toolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpClient struct {
	endpoint        string
	token           string
	httpClient      *http.Client
	sessionID       string
	protocolVersion string
	nextID          atomic.Int64
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type mcpToolResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
}

// ListBugs lists visible bugs across the account's workspaces.
func (c *Client) ListBugs(ctx context.Context) ([]Bug, error) {
	if err := c.cfg.Validate(); err != nil {
		return nil, err
	}
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = DefaultMCPURL
	}
	mcp := &mcpClient{endpoint: endpoint, token: c.cfg.AccessToken, httpClient: c.httpClient}
	if err := mcp.initialize(ctx); err != nil {
		return nil, fmt.Errorf("initialize TAPD MCP: %w", err)
	}
	tools, err := mcp.listTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list TAPD MCP tools: %w", err)
	}
	workspaceTool := findTool(tools, "list_workspaces", "tapd_list_workspaces", "get_user_participant_projects", "workspace_list", "list_projects", "get_projects")
	bugTool := findTool(tools, "get_bugs_list", "list_bugs", "tapd_list_bugs", "bug_list", "get_bugs", "get_bug_list", "get_bug", "bugs_list")
	if bugTool == nil {
		return nil, fmt.Errorf("TAPD MCP does not expose a bug-list tool")
	}
	if !toolAcceptsWorkspace(*bugTool) {
		bugs, err := mcp.listBugs(ctx, *bugTool, "", c.cfg.Statuses, c.cfg.Fields)
		if err != nil {
			return nil, fmt.Errorf("list TAPD MCP bugs: %w", err)
		}
		return normalizeBugs(bugs, ""), nil
	}
	if workspaceTool == nil {
		return nil, fmt.Errorf("TAPD MCP does not expose a workspace-list tool")
	}
	workspaces, err := mcp.listWorkspaces(ctx, *workspaceTool)
	if err != nil {
		return nil, err
	}
	if len(workspaces) == 0 {
		return []Bug{}, nil
	}
	var result []Bug
	seen := map[string]struct{}{}
	for _, workspaceID := range workspaces {
		bugs, err := mcp.listBugs(ctx, *bugTool, workspaceID, c.cfg.Statuses, c.cfg.Fields)
		if err != nil {
			return nil, fmt.Errorf("list TAPD MCP bugs for workspace %s: %w", workspaceID, err)
		}
		result = appendUniqueBugs(result, seen, bugs, workspaceID)
	}
	return result, nil
}

// ListMyBugs returns the current TAPD account's pending bug work items across
// all projects visible to the account. TAPD's get_todo tool applies the
// account-specific ownership filter on the server side.
func (c *Client) ListMyBugs(ctx context.Context) ([]Bug, error) {
	if err := c.cfg.Validate(); err != nil {
		return nil, err
	}
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = DefaultMCPURL
	}
	mcp := &mcpClient{endpoint: endpoint, token: c.cfg.AccessToken, httpClient: c.httpClient}
	if err := mcp.initialize(ctx); err != nil {
		return nil, fmt.Errorf("initialize TAPD MCP: %w", err)
	}
	tools, err := mcp.listTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list TAPD MCP tools: %w", err)
	}
	workspaceTool := findTool(tools, "list_workspaces", "tapd_list_workspaces", "get_user_participant_projects", "workspace_list", "list_projects", "get_projects")
	todoTool := findTool(tools, "get_todo", "get_user_todo", "list_todo", "todo_list")
	if todoTool == nil {
		return nil, fmt.Errorf("TAPD MCP does not expose a todo-list tool")
	}
	if !toolAcceptsWorkspace(*todoTool) {
		bugs, err := mcp.listMyBugs(ctx, *todoTool, "")
		if err != nil {
			return nil, fmt.Errorf("list TAPD my bugs: %w", err)
		}
		return normalizeBugs(bugs, ""), nil
	}
	if workspaceTool == nil {
		return nil, fmt.Errorf("TAPD MCP does not expose a workspace-list tool")
	}
	workspaces, err := mcp.listWorkspaces(ctx, *workspaceTool)
	if err != nil {
		return nil, err
	}
	var result []Bug
	seen := map[string]struct{}{}
	for _, workspaceID := range workspaces {
		bugs, err := mcp.listMyBugs(ctx, *todoTool, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("list TAPD my bugs for workspace %s: %w", workspaceID, err)
		}
		result = appendUniqueBugs(result, seen, bugs, workspaceID)
	}
	return result, nil
}

func (m *mcpClient) initialize(ctx context.Context) error {
	result, err := m.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "tapd-dingding", "version": "1.0.0"},
	})
	if err != nil {
		return err
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(result, &initialized); err != nil {
		return fmt.Errorf("decode initialize response: %w", err)
	}
	m.protocolVersion = initialized.ProtocolVersion
	if m.protocolVersion == "" {
		m.protocolVersion = "2025-06-18"
	}
	if err := m.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	return nil
}

func (m *mcpClient) listTools(ctx context.Context) ([]toolInfo, error) {
	var tools []toolInfo
	var cursor string
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := m.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var response struct {
			Tools      []toolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &response); err != nil {
			return nil, fmt.Errorf("decode tools/list response: %w", err)
		}
		tools = append(tools, response.Tools...)
		if response.NextCursor == "" || response.NextCursor == cursor {
			return tools, nil
		}
		cursor = response.NextCursor
	}
	return nil, fmt.Errorf("TAPD MCP tools/list exceeded pagination limit")
}

func (m *mcpClient) listWorkspaces(ctx context.Context, tool toolInfo) ([]string, error) {
	result, err := m.callTool(ctx, tool, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", tool.Name, err)
	}
	values := toolValues(result)
	seen := map[string]bool{}
	var ids []string
	for _, value := range values {
		collectWorkspaceIDs(value, &ids, seen)
	}
	return ids, nil
}

func (m *mcpClient) listBugs(ctx context.Context, tool toolInfo, workspaceID string, statuses []string, fields string) ([]Bug, error) {
	limit := 200
	var result []Bug
	seen := map[string]bool{}
	for page := 1; page <= 100; page++ {
		before := len(result)
		args := buildBugArguments(tool, workspaceID, statuses, fields, page, limit)
		payload, err := m.callTool(ctx, tool, args)
		if err != nil {
			return nil, fmt.Errorf("call %s: %w", tool.Name, err)
		}
		bugs := extractBugs(toolValues(payload))
		for _, bug := range bugs {
			key := bugKey(bug, "")
			if key != "" && !seen[key] {
				seen[key] = true
				result = append(result, bug)
			}
		}
		if len(bugs) < limit || len(result) == before {
			return result, nil
		}
	}
	return nil, fmt.Errorf("TAPD MCP bug-list exceeded pagination limit")
}

func (m *mcpClient) listMyBugs(ctx context.Context, tool toolInfo, workspaceID string) ([]Bug, error) {
	const limit int64 = 200
	var result []Bug
	seen := map[string]bool{}
	for page := int64(1); page <= 100; page++ {
		args := buildTodoArguments(tool, workspaceID, page, limit)
		payload, err := m.callTool(ctx, tool, args)
		if err != nil {
			return nil, fmt.Errorf("call %s: %w", tool.Name, err)
		}
		bugs := extractBugs(toolValues(payload))
		for _, bug := range bugs {
			key := bugKey(bug, workspaceID)
			if key != "" && !seen[key] {
				seen[key] = true
				result = append(result, bug)
			}
		}
		if len(bugs) < int(limit) {
			return result, nil
		}
	}
	return nil, fmt.Errorf("TAPD MCP todo-list exceeded pagination limit")
}

func normalizeBugs(bugs []Bug, fallbackWorkspace string) []Bug {
	return appendUniqueBugs(nil, make(map[string]struct{}), bugs, fallbackWorkspace)
}

func appendUniqueBugs(result []Bug, seen map[string]struct{}, bugs []Bug, fallbackWorkspace string) []Bug {
	for _, bug := range bugs {
		if bug.ID == "" {
			continue
		}
		if bug.WorkspaceID == "" {
			bug.WorkspaceID = fallbackWorkspace
		}
		key := bugKey(bug, fallbackWorkspace)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, bug)
	}
	return result
}

func bugKey(bug Bug, fallbackWorkspace string) string {
	if bug.ID == "" {
		return ""
	}
	workspaceID := bug.WorkspaceID
	if workspaceID == "" {
		workspaceID = fallbackWorkspace
	}
	return workspaceID + "\x00" + bug.ID
}

func toolAcceptsWorkspace(tool toolInfo) bool {
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	if len(properties) == 0 {
		return false
	}
	for _, name := range []string{"workspace_id", "workspaceId", "workspace", "workspace_ids", "workspaceIds"} {
		if _, ok := properties[name]; ok {
			return true
		}
	}
	return false
}

func buildBugArguments(tool toolInfo, workspaceID string, statuses []string, fields string, page, limit int) map[string]any {
	args := map[string]any{}
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	if _, ok := properties["options"]; ok {
		if key := firstProperty(properties, "workspace_id", "workspaceId", "workspace"); key != "" {
			args[key] = workspaceID
		} else if key := firstProperty(properties, "workspace_ids", "workspaceIds"); key != "" && workspaceID != "" {
			args[key] = []string{workspaceID}
		}
		options := map[string]any{"page": page, "limit": limit}
		if len(statuses) > 0 {
			options["status"] = strings.Join(statuses, "|")
		}
		if fields != "" {
			options["fields"] = fields
		}
		args["options"] = options
		return args
	}
	accepts := func(names ...string) string {
		if len(properties) == 0 {
			return ""
		}
		for _, name := range names {
			if _, ok := properties[name]; ok {
				return name
			}
		}
		return ""
	}
	if key := accepts("workspace_id", "workspaceId", "workspace"); key != "" {
		args[key] = workspaceID
	} else if key := accepts("workspace_ids", "workspaceIds"); key != "" && workspaceID != "" {
		args[key] = []string{workspaceID}
	}
	if len(statuses) > 0 {
		if key := accepts("statuses", "status"); key != "" {
			if key == "statuses" {
				args[key] = statuses
			} else {
				args[key] = strings.Join(statuses, "|")
			}
		}
	}
	if fields != "" {
		if key := accepts("fields", "show_fields"); key != "" {
			args[key] = fields
		}
	}
	if key := accepts("page", "page_number"); key != "" {
		args[key] = page
	}
	if key := accepts("limit", "page_size", "per_page"); key != "" {
		args[key] = limit
	}
	return args
}

func buildTodoArguments(tool toolInfo, workspaceID string, page, limit int64) map[string]any {
	args := map[string]any{}
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	if key := firstProperty(properties, "workspace_id", "workspaceId", "workspace"); key != "" {
		if value, err := strconv.ParseInt(workspaceID, 10, 64); err == nil {
			args[key] = value
		} else {
			args[key] = workspaceID
		}
	} else if key := firstProperty(properties, "workspace_ids", "workspaceIds"); key != "" && workspaceID != "" {
		args[key] = []string{workspaceID}
	}
	if _, ok := properties["entity_type"]; ok {
		args["entity_type"] = "bug"
	}
	if _, ok := properties["page"]; ok {
		args["page"] = page
	}
	if _, ok := properties["limit"]; ok {
		args["limit"] = limit
	}
	return args
}

func firstProperty(properties map[string]any, names ...string) string {
	for _, name := range names {
		if _, ok := properties[name]; ok {
			return name
		}
	}
	return ""
}

func findTool(tools []toolInfo, candidates ...string) *toolInfo {
	for _, candidate := range candidates {
		for i := range tools {
			if strings.EqualFold(tools[i].Name, candidate) {
				return &tools[i]
			}
		}
	}
	for i := range tools {
		text := strings.ToLower(tools[i].Name + " " + tools[i].Description)
		if containsAny(text, "workspace", "project", "项目", "空间") && containsAny(text, "list", "query", "search", "get", "获取", "查询", "列表") {
			for _, candidate := range candidates {
				if strings.Contains(candidate, "workspace") || strings.Contains(candidate, "project") {
					return &tools[i]
				}
			}
		}
		if containsAny(text, "bug", "缺陷") && containsAny(text, "list", "query", "search", "get", "获取", "查询", "列表") && !containsAny(text, "detail", "详情") {
			for _, candidate := range candidates {
				if strings.Contains(candidate, "bug") {
					return &tools[i]
				}
			}
		}
	}
	return nil
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func (m *mcpClient) callTool(ctx context.Context, tool toolInfo, args map[string]any) (json.RawMessage, error) {
	result, err := m.request(ctx, "tools/call", map[string]any{"name": tool.Name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var toolResult mcpToolResult
	if err := json.Unmarshal(result, &toolResult); err != nil {
		return nil, fmt.Errorf("decode %s result: %w", tool.Name, err)
	}
	if toolResult.IsError {
		var details []string
		for _, content := range toolResult.Content {
			if content.Text != "" {
				details = append(details, content.Text)
			}
		}
		return nil, fmt.Errorf("tool returned an error: %s", truncate(strings.Join(details, "; "), 500))
	}
	return result, nil
}

func (m *mcpClient) notify(ctx context.Context, method string, params map[string]any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	m.setHeaders(req)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request MCP notification: %w", err)
	}
	defer resp.Body.Close()
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		m.sessionID = sessionID
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return fmt.Errorf("read MCP notification response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MCP notification returned HTTP %d: %s", resp.StatusCode, truncate(string(responseBody), 500))
	}
	return nil
}

func (m *mcpClient) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	id := m.nextID.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	m.setHeaders(req)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request MCP: %w", err)
	}
	defer resp.Body.Close()
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		m.sessionID = sessionID
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, fmt.Errorf("read MCP response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP returned HTTP %d: %s", resp.StatusCode, truncate(string(responseBody), 500))
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return nil, nil
	}
	message, err := decodeRPCResponse(resp.Header.Get("Content-Type"), responseBody)
	if err != nil {
		return nil, err
	}
	if len(message.ID) > 0 && string(bytes.TrimSpace(message.ID)) != strconv.FormatInt(id, 10) {
		return nil, fmt.Errorf("MCP response id mismatch: got %s, want %d", message.ID, id)
	}
	if message.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", message.Error.Code, message.Error.Message)
	}
	return message.Result, nil
}

func (m *mcpClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if m.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", m.sessionID)
	}
	if m.protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", m.protocolVersion)
	}
}

func decodeRPCResponse(contentType string, body []byte) (rpcResponse, error) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") || looksLikeEventStream(body) {
		var last []byte
		scanner := bufio.NewScanner(bytes.NewReader(body))
		scanner.Buffer(make([]byte, 64*1024), 64<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "data:") {
				last = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err := scanner.Err(); err != nil {
			return rpcResponse{}, fmt.Errorf("read MCP event stream: %w", err)
		}
		if len(last) == 0 {
			return rpcResponse{}, fmt.Errorf("MCP event stream contained no JSON response")
		}
		body = last
	}
	var response rpcResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return rpcResponse{}, fmt.Errorf("decode MCP JSON-RPC response: %w", err)
	}
	return response, nil
}

func looksLikeEventStream(body []byte) bool {
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		return bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte(":"))
	}
	return false
}

func toolValues(raw json.RawMessage) []any {
	var result mcpToolResult
	if json.Unmarshal(raw, &result) == nil {
		var values []any
		if len(result.StructuredContent) > 0 && string(result.StructuredContent) != "null" {
			var value any
			if json.Unmarshal(result.StructuredContent, &value) == nil {
				values = append(values, value)
			}
		}
		for _, content := range result.Content {
			if content.Type != "text" {
				continue
			}
			var value any
			if json.Unmarshal([]byte(content.Text), &value) == nil {
				values = append(values, value)
			}
		}
		if len(values) > 0 {
			return values
		}
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		return []any{value}
	}
	return nil
}

func collectWorkspaceIDs(value any, result *[]string, seen map[string]bool) {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			collectWorkspaceIDs(item, result, seen)
		}
	case map[string]any:
		if strings.EqualFold(stringValue(current["category"]), "organization") {
			return
		}
		for _, key := range []string{"workspace_id", "workspaceId", "id"} {
			if id, ok := current[key]; ok {
				if text := stringValue(id); text != "" && (key != "id" || hasWorkspaceHint(current)) && !seen[text] {
					seen[text] = true
					*result = append(*result, text)
				}
			}
		}
		for _, item := range current {
			collectWorkspaceIDs(item, result, seen)
		}
	}
}

func hasWorkspaceHint(value map[string]any) bool {
	for key := range value {
		key = strings.ToLower(key)
		if strings.Contains(key, "workspace") || strings.Contains(key, "project") || key == "name" || key == "title" {
			return true
		}
	}
	return false
}

func extractBugs(values []any) []Bug {
	var result []Bug
	seen := map[string]bool{}
	var visit func(any)
	visit = func(value any) {
		switch current := value.(type) {
		case []any:
			for _, item := range current {
				visit(item)
			}
		case map[string]any:
			if nested, ok := current["Bug"]; ok {
				visit(nested)
				return
			}
			if id := stringValue(current["id"]); id != "" && (current["title"] != nil || current["status"] != nil || current["workspace_id"] != nil || current["workspaceId"] != nil) {
				encoded, err := json.Marshal(current)
				if err == nil {
					var bug Bug
					if json.Unmarshal(encoded, &bug) == nil && !seen[bug.ID] {
						seen[bug.ID] = true
						result = append(result, bug)
					}
				}
			}
			for _, item := range current {
				visit(item)
			}
		}
	}
	for _, value := range values {
		visit(value)
	}
	return result
}

func stringValue(value any) string {
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current)
	case float64:
		return strconv.FormatInt(int64(current), 10)
	case json.Number:
		return current.String()
	default:
		if value != nil {
			return strings.TrimSpace(fmt.Sprint(value))
		}
		return ""
	}
}

func (b *Bug) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	get := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := raw[key]; ok && value != nil {
				if text := stringValue(value); text != "" {
					return text
				}
			}
		}
		return ""
	}
	b.ID, b.Title, b.Description = get("id"), get("title", "name"), get("description")
	b.Priority, b.PriorityLabel, b.Severity, b.Module, b.Status = get("priority"), get("priority_label", "priorityLabel"), get("severity"), get("module"), get("status")
	b.Reporter, b.CurrentOwner, b.De, b.Fixer, b.Te, b.Confirmer, b.Cc, b.Participator = get("reporter"), get("current_owner", "currentOwner"), get("de"), get("fixer"), get("te"), get("confirmer"), get("cc"), get("participator")
	b.Created, b.Modified, b.WorkspaceID = get("created"), get("modified"), get("workspace_id", "workspaceId")
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
