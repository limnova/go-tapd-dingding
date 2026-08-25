package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tapd-dingding/internal/database"
	"tapd-dingding/internal/dingtalk"
	"tapd-dingding/internal/tapd"
)

// Handler 返回健康检查、指标和连接管理接口。
// 连接管理接口会接收敏感凭据，应通过部署环境的网络策略或网关保护，
// 不应直接暴露到公网。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method must be GET")
			return
		}
		writeText(w, http.StatusOK, "ok\n")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method must be GET")
			return
		}
		if s.db == nil {
			http.Error(w, "database not ready", http.StatusServiceUnavailable)
			return
		}
		if err := s.db.Ready(r.Context()); err != nil {
			http.Error(w, "database not ready", http.StatusServiceUnavailable)
			return
		}
		writeText(w, http.StatusOK, "ready\n")
	})
	mux.HandleFunc("/api/connections/tapd", s.handleTapdConnection)
	mux.HandleFunc("/api/connections/dingtalk", s.handleDingTalkConnection)
	mux.HandleFunc("/api/recipients/tapd", s.handleTapdRecipient)
	mux.HandleFunc("/metrics", s.metrics)
	return s.accessLogMiddleware(mux)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (s *Service) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		started := time.Now()
		writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(writer, r)
		level := slog.LevelInfo
		if writer.status >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if writer.status >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		s.logger.Log(r.Context(), level, "HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", writer.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"content_length", r.ContentLength,
		)
	})
}

type tapdConnectionRequest struct {
	Name        string   `json:"name"`
	AccessToken string   `json:"access_token"`
	Statuses    []string `json:"statuses"`
	Fields      string   `json:"fields"`
}

type dingtalkConnectionRequest struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

type tapdRecipientRequest struct {
	TapdConnectionID int64  `json:"tapd_connection_id"`
	TapdAccount      string `json:"tapd_account"`
	Name             string `json:"name"`
	DingTalkUserID   string `json:"dingtalk_user_id"`
	DingTalkMobile   string `json:"dingtalk_mobile"`
}

func (s *Service) handleTapdConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method must be POST")
		return
	}
	var request tapdConnectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	connection := tapd.Connection{
		Name: request.Name, AccessToken: request.AccessToken,
		Statuses: request.Statuses, Fields: request.Fields,
	}
	if err := connection.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.db.UpsertTapdConnection(r.Context(), connection, s.box)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": connection.Name})
}

func (s *Service) handleDingTalkConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method must be POST")
		return
	}
	var request dingtalkConnectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	connection := dingtalk.Connection{Name: request.Name, URL: request.URL, Secret: request.Secret}
	if err := connection.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.db.UpsertDingTalkConnection(r.Context(), connection, s.box)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": connection.Name})
}

func (s *Service) handleTapdRecipient(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		connectionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("tapd_connection_id")), 10, 64)
		if err != nil || connectionID <= 0 {
			writeAPIError(w, http.StatusBadRequest, "tapd_connection_id must be a positive integer")
			return
		}
		recipients, err := s.db.ListTapdRecipients(r.Context(), connectionID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tapd_connection_id": connectionID, "recipients": recipients})
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method must be GET or POST")
		return
	}
	var request tapdRecipientRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.db.UpsertTapdRecipient(r.Context(), database.TapdRecipientMapping{
		TapdConnectionID: request.TapdConnectionID,
		TapdAccount:      request.TapdAccount,
		Name:             request.Name,
		DingTalkUserID:   request.DingTalkUserID,
		DingTalkMobile:   request.DingTalkMobile,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "tapd_connection_id": request.TapdConnectionID, "tapd_account": request.TapdAccount})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("decode request JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain one JSON object")
		}
		return fmt.Errorf("read request body: %w", err)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return fmt.Errorf("request body must contain one JSON object")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request JSON: %w", err)
	}
	return nil
}

func writeText(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
