package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/tokenutil"
)

type accountView struct {
	ID                 int64   `json:"id"`
	Email              string  `json:"email"`
	AccessTokenPreview string  `json:"access_token_preview"`
	HasProxy           bool    `json:"has_proxy"`
	ProxyPreview       string  `json:"proxy_preview"`
	Status             string  `json:"status"`
	Disabled           bool    `json:"disabled"`
	Available          bool    `json:"available"`
	InvalidAt          float64 `json:"invalid_at"`
	LastUsedAt         string  `json:"last_used_at"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	TokenHasExp        bool    `json:"token_has_exp"`
	TokenExp           int64   `json:"token_exp,omitempty"`
	ExpiresInSeconds   *int64  `json:"expires_in_seconds,omitempty"`
	TokenExpired       bool    `json:"token_expired,omitempty"`
}

type accountWriteRequest struct {
	Email       string  `json:"email"`
	AccessToken *string `json:"access_token"`
	Proxy       *string `json:"proxy"`
	Status      string  `json:"status"`
	Disabled    bool    `json:"disabled"`
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	items, err := s.accounts.List()
	if err != nil {
		writeAccountError(w, r, err)
		return
	}
	stats, err := s.accounts.Stats()
	if err != nil {
		writeAccountError(w, r, err)
		return
	}
	views := make([]accountView, 0, len(items))
	for _, item := range items {
		views = append(views, newAccountView(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": views,
		"stats":    stats,
	})
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	request, err := decodeAccountWriteRequest(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	if request.AccessToken == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": "access_token is required"}})
		return
	}
	account := accounts.Account{
		Email:       request.Email,
		AccessToken: *request.AccessToken,
		Status:      request.Status,
		Disabled:    request.Disabled,
	}
	if request.Proxy != nil {
		account.Proxy = *request.Proxy
	}
	created, err := s.accounts.Create(account)
	if err != nil {
		writeAccountError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("account_created", "account_id", created.ID, "available", !created.Disabled)
	writeJSON(w, http.StatusCreated, map[string]any{"account": newAccountView(created)})
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathAccountID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	request, err := decodeAccountWriteRequest(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	updated, err := s.accounts.Update(id, accounts.AccountUpdate{
		Email:       request.Email,
		AccessToken: request.AccessToken,
		Proxy:       request.Proxy,
		Status:      request.Status,
		Disabled:    request.Disabled,
	})
	if err != nil {
		writeAccountError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("account_updated", "account_id", updated.ID, "available", !updated.Disabled)
	writeJSON(w, http.StatusOK, map[string]any{"account": newAccountView(updated)})
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathAccountID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	if err := s.accounts.Delete(id); err != nil {
		writeAccountError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("account_deleted", "account_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) checkAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathAccountID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	if s.voice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": map[string]any{"error": "voice service unavailable"}})
		return
	}
	result, err := s.voice.ProbeAccountToken(id)
	if err != nil {
		writeAccountError(w, r, err)
		return
	}
	account, err := s.accounts.Get(id)
	if err != nil {
		writeAccountError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info(
		"account_probe_completed",
		"account_id", id,
		"status", result.Status,
		"alive", result.Alive,
		"status_code", result.StatusCode,
		"marked_invalid", result.MarkedInvalid,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"probe":   result,
		"account": newAccountView(account),
	})
}

func decodeAccountWriteRequest(w http.ResponseWriter, r *http.Request) (accountWriteRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 160<<10)
	var request accountWriteRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return accountWriteRequest{}, fmt.Errorf("invalid account payload")
	}
	request.Email = strings.TrimSpace(request.Email)
	request.Status = strings.TrimSpace(request.Status)
	if request.Status == "" {
		request.Status = "正常"
	}
	if len(request.Email) > 320 {
		return accountWriteRequest{}, fmt.Errorf("email is too long")
	}
	if len(request.Status) > 64 {
		return accountWriteRequest{}, fmt.Errorf("status is too long")
	}
	if request.AccessToken != nil {
		value := strings.TrimSpace(*request.AccessToken)
		if value == "" {
			return accountWriteRequest{}, fmt.Errorf("access_token cannot be empty")
		}
		if len(value) > 64<<10 {
			return accountWriteRequest{}, fmt.Errorf("access_token is too long")
		}
		request.AccessToken = &value
	}
	if request.Proxy != nil {
		value := strings.TrimSpace(*request.Proxy)
		if len(value) > 4096 {
			return accountWriteRequest{}, fmt.Errorf("proxy is too long")
		}
		request.Proxy = &value
	}
	return request, nil
}

func pathAccountID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("invalid account id")
	}
	return id, nil
}

func newAccountView(account accounts.Account) accountView {
	available := !account.Disabled && account.Status != "禁用"
	view := accountView{
		ID:                 account.ID,
		Email:              account.Email,
		AccessTokenPreview: secretPreview(account.AccessToken),
		HasProxy:           account.Proxy != "",
		ProxyPreview:       proxyPreview(account.Proxy),
		Status:             account.Status,
		Disabled:           account.Disabled,
		Available:          available,
		InvalidAt:          account.InvalidAt,
		LastUsedAt:         account.LastUsedAt,
		CreatedAt:          account.CreatedAt,
		UpdatedAt:          account.UpdatedAt,
	}
	if expiry, err := tokenutil.ParseAccessTokenExpiry(account.AccessToken); err == nil {
		view.TokenHasExp = expiry.HasExp
		view.TokenExp = expiry.Exp
		view.TokenExpired = expiry.Expired
		if expiry.HasExp {
			seconds := expiry.ExpiresInSeconds
			view.ExpiresInSeconds = &seconds
		}
	}
	return view
}

func secretPreview(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 10 {
		return "••••••"
	}
	return value[:6] + "…" + value[len(value)-4:]
}

func proxyPreview(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "已配置"
	}
	if parsed.Scheme == "" {
		return parsed.Host
	}
	return parsed.Scheme + "://" + parsed.Host
}

func writeAccountError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"error": "account not found"}})
	case errors.Is(err, accounts.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"detail": map[string]any{"error": "access_token already exists"}})
	default:
		var validationError *accounts.Error
		if errors.As(err, &validationError) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": validationError.Message}})
			return
		}
		logging.FromContext(r.Context()).Error("account_operation_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": map[string]any{"error": "account operation failed"}})
	}
}
