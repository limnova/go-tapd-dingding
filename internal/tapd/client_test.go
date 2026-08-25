package tapd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListBugsThroughMCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer personal-token" {
			t.Fatalf("unexpected auth header")
		}
		var request struct {
			Method string         `json:"method"`
			ID     int64          `json:"id"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "initialize" && r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
			t.Fatalf("missing MCP protocol version header for %s", request.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"list_workspaces","inputSchema":{"type":"object"}},{"name":"get_bugs_list","inputSchema":{"type":"object","properties":{"workspace_id":{"type":"string"},"page":{"type":"integer"},"limit":{"type":"integer"}}}}]}}`))
		case "tools/call":
			params := request.Params
			name, _ := params["name"].(string)
			if name == "list_workspaces" {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"structuredContent":{"workspaces":[{"id":"w1","name":"研发"}]}}}`))
				return
			}
			if name == "get_bugs_list" {
				args, _ := params["arguments"].(map[string]any)
				if args["workspace_id"] != "w1" {
					t.Fatalf("workspace was not passed to MCP tool: %v", args)
				}
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"{\"bugs\":[{\"id\":\"1\",\"title\":\"bug\",\"status\":\"new\",\"workspace_id\":\"w1\"}]}"}]}}`))
				return
			}
			t.Fatalf("unexpected MCP tool: %q", name)
		default:
			t.Fatalf("unexpected MCP method: %q", request.Method)
		}
	}))
	defer server.Close()

	client := NewClient(Connection{Name: "test", AccessToken: "personal-token"})
	client.endpoint = server.URL
	bugs, err := client.ListBugs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bugs) != 1 || bugs[0].ID != "1" || bugs[0].Title != "bug" || bugs[0].WorkspaceID != "w1" {
		t.Fatalf("unexpected bugs: %+v", bugs)
	}
}

func TestConnectionValidateRequiresMCPFields(t *testing.T) {
	if err := (Connection{Name: "test"}).Validate(); err == nil {
		t.Fatal("missing MCP fields were accepted")
	}
	if err := (Connection{Name: "test", AccessToken: "token"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestListBugsWithGlobalToolDoesNotRequireWorkspaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"list_bugs","inputSchema":{"type":"object"}}]}}`))
		case "tools/call":
			if name, _ := request.Params["name"].(string); name != "list_bugs" {
				t.Fatalf("unexpected tool: %q", name)
			}
			args, _ := request.Params["arguments"].(map[string]any)
			if len(args) != 0 {
				t.Fatalf("global tool received workspace arguments: %v", args)
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"bugs\":[{\"id\":\"1\",\"title\":\"global bug\",\"status\":\"open\"}]}"}]}}`))
		default:
			t.Fatalf("unexpected MCP method: %q", request.Method)
		}
	}))
	defer server.Close()

	client := NewClient(Connection{Name: "test", AccessToken: "token"})
	client.endpoint = server.URL
	bugs, err := client.ListBugs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bugs) != 1 || bugs[0].ID != "1" || bugs[0].Title != "global bug" {
		t.Fatalf("unexpected bugs: %+v", bugs)
	}
}
