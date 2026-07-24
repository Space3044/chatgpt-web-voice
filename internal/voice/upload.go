package voice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/httpclient"
)

const (
	filesURL             = "https://chatgpt.com/backend-api/files"
	filesUploadTimeout   = 60 * time.Second
	filesUploadBodyLimit = 1 << 20
	maxImageUploadBytes  = 20 << 20 // 20 MiB; matches common ChatGPT multimodal caps
	maxImageNameRunes    = 180
)

// ImageUploadRequest is metadata the client will PUT to Azure.
// The gateway never receives image bytes.
type ImageUploadRequest struct {
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// ImageUploadCredential is a short-lived direct-upload ticket for the sticky
// account bound to voice_session_id. No image metadata is stored on the gateway.
type ImageUploadCredential struct {
	VoiceSessionID  string            `json:"voice_session_id"`
	FileID          string            `json:"file_id"`
	UploadURL       string            `json:"upload_url"`
	UploadMethod    string            `json:"upload_method"`
	RequiredHeaders map[string]string `json:"required_headers"`
	FileName        string            `json:"file_name"`
	MimeType        string            `json:"mime_type"`
	FileSize        int64             `json:"file_size"`
	Width           int               `json:"width,omitempty"`
	Height          int               `json:"height,omitempty"`
	// AssetPointer is the DataChannel image_asset_pointer value after upload.
	AssetPointer string `json:"asset_pointer"`
}

// ImageUploadCompleteResult acknowledges ChatGPT file finalization.
// The gateway does not retain file_id after the response.
type ImageUploadCompleteResult struct {
	VoiceSessionID string `json:"voice_session_id"`
	FileID         string `json:"file_id"`
	AssetPointer   string `json:"asset_pointer"`
	Completed      bool   `json:"completed"`
}

// CreateImageUploadCredential asks chatgpt.com for an Azure SAS upload URL using
// the sticky account on the live voice_session binding. Image bytes never touch
// the gateway; nothing is written to SQLite.
func (s *Service) CreateImageUploadCredential(owner, voiceSessionID string, req ImageUploadRequest) (*ImageUploadCredential, error) {
	owner = normalizeSessionOwner(owner)
	sessionID := strings.TrimSpace(voiceSessionID)
	if sessionID == "" {
		return nil, &ServiceError{Message: "voice session id is required", StatusCode: 400}
	}

	meta, err := normalizeImageUploadRequest(req)
	if err != nil {
		return nil, err
	}

	binding, ownedByOther := s.boundVoiceSession(owner, sessionID)
	if ownedByOther {
		return nil, &ServiceError{Message: "voice session does not belong to caller", StatusCode: 403}
	}
	if binding == nil {
		// Uploads require a live in-memory binding (active call). Durable
		// call_sessions alone is not enough — there is no sticky token without
		// a bound session, and we do not re-pick accounts for orphan uploads.
		return nil, &ServiceError{Message: "voice session not found", StatusCode: 404}
	}
	if strings.TrimSpace(binding.AccessToken) == "" {
		return nil, &ServiceError{Message: "voice session has no sticky account token", StatusCode: 404}
	}

	status, body, err := s.postFilesCreateOnce(binding.AccessToken, binding.Proxy, meta)
	if err != nil {
		s.logger.Warn("image_upload_create_failed", "voice_session_id", sessionID, "error", err)
		return nil, &ServiceError{
			Message:    "upstream files request failed",
			StatusCode: 502,
			Detail:     truncate(err.Error(), 300),
		}
	}
	switch status {
	case http.StatusUnauthorized:
		if markErr := s.pool.MarkInvalid(binding.AccessToken); markErr != nil {
			s.logger.Error("account_mark_invalid_failed", "account_id", binding.AccountID, "error", markErr)
		}
		return nil, &ServiceError{Message: "account token invalid", StatusCode: 401, Detail: truncate(body, 300)}
	case http.StatusForbidden:
		return nil, &ServiceError{Message: "upstream files request forbidden", StatusCode: 403, Detail: truncate(body, 300)}
	case http.StatusTooManyRequests:
		return nil, &ServiceError{Message: "upstream files rate limited", StatusCode: 429, Detail: truncate(body, 300)}
	case http.StatusOK, http.StatusCreated:
		// continue
	default:
		return nil, &ServiceError{
			Message:    fmt.Sprintf("upstream files request failed status=%d", status),
			StatusCode: 502,
			Detail:     truncate(body, 300),
		}
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil, &ServiceError{Message: "upstream files response invalid", StatusCode: 502, Detail: truncate(body, 200)}
	}
	fileID := strings.TrimSpace(fmt.Sprint(payload["file_id"]))
	uploadURL := strings.TrimSpace(fmt.Sprint(payload["upload_url"]))
	if fileID == "" || fileID == "<nil>" || uploadURL == "" || uploadURL == "<nil>" {
		return nil, &ServiceError{Message: "upstream files response missing upload_url/file_id", StatusCode: 502, Detail: truncate(body, 200)}
	}
	if !validUpstreamFileID(fileID) {
		return nil, &ServiceError{Message: "upstream file id is invalid", StatusCode: 502}
	}

	s.logger.Info(
		"image_upload_credential_issued",
		"voice_session_id", sessionID,
		"account_id", binding.AccountID,
		"file_id", fileID,
		"file_size", meta.FileSize,
		"mime_type", meta.MimeType,
	)

	return &ImageUploadCredential{
		VoiceSessionID: sessionID,
		FileID:         fileID,
		UploadURL:      uploadURL,
		UploadMethod:   http.MethodPut,
		RequiredHeaders: map[string]string{
			"Content-Type":   meta.MimeType,
			"x-ms-blob-type": "BlockBlob",
			"x-ms-version":   "2020-04-08",
		},
		FileName:     meta.FileName,
		MimeType:     meta.MimeType,
		FileSize:     meta.FileSize,
		Width:        meta.Width,
		Height:       meta.Height,
		AssetPointer: "sediment://" + fileID,
	}, nil
}

// CompleteImageUpload marks a direct-uploaded blob as ready on chatgpt.com using
// the sticky session account. Stateless: no file rows are stored on the gateway.
func (s *Service) CompleteImageUpload(owner, voiceSessionID, fileID string) (*ImageUploadCompleteResult, error) {
	owner = normalizeSessionOwner(owner)
	sessionID := strings.TrimSpace(voiceSessionID)
	fileID = strings.TrimSpace(fileID)
	if sessionID == "" {
		return nil, &ServiceError{Message: "voice session id is required", StatusCode: 400}
	}
	if !validUpstreamFileID(fileID) {
		return nil, &ServiceError{Message: "file id is invalid", StatusCode: 400}
	}

	binding, ownedByOther := s.boundVoiceSession(owner, sessionID)
	if ownedByOther {
		return nil, &ServiceError{Message: "voice session does not belong to caller", StatusCode: 403}
	}
	if binding == nil {
		return nil, &ServiceError{Message: "voice session not found", StatusCode: 404}
	}
	if strings.TrimSpace(binding.AccessToken) == "" {
		return nil, &ServiceError{Message: "voice session has no sticky account token", StatusCode: 404}
	}

	status, body, err := s.postFileUploadedOnce(binding.AccessToken, binding.Proxy, fileID)
	if err != nil {
		s.logger.Warn("image_upload_complete_failed", "voice_session_id", sessionID, "file_id", fileID, "error", err)
		return nil, &ServiceError{
			Message:    "upstream file complete failed",
			StatusCode: 502,
			Detail:     truncate(err.Error(), 300),
		}
	}
	switch status {
	case http.StatusUnauthorized:
		if markErr := s.pool.MarkInvalid(binding.AccessToken); markErr != nil {
			s.logger.Error("account_mark_invalid_failed", "account_id", binding.AccountID, "error", markErr)
		}
		return nil, &ServiceError{Message: "account token invalid", StatusCode: 401, Detail: truncate(body, 300)}
	case http.StatusOK, http.StatusCreated:
		// done
	default:
		// Fallback used by some ChatGPT deployments when /uploaded is unavailable.
		processStatus, processBody, processErr := s.postProcessUploadStreamOnce(binding.AccessToken, binding.Proxy, fileID)
		if processErr != nil {
			return nil, &ServiceError{
				Message:    "upstream file complete failed",
				StatusCode: 502,
				Detail:     truncate(processErr.Error(), 300),
			}
		}
		if processStatus != http.StatusOK && processStatus != http.StatusCreated {
			return nil, &ServiceError{
				Message:    fmt.Sprintf("upstream file complete failed status=%d", status),
				StatusCode: 502,
				Detail:     truncate(firstNonEmpty(processBody, body), 300),
			}
		}
	}

	s.logger.Info("image_upload_completed", "voice_session_id", sessionID, "account_id", binding.AccountID, "file_id", fileID)
	return &ImageUploadCompleteResult{
		VoiceSessionID: sessionID,
		FileID:         fileID,
		AssetPointer:   "sediment://" + fileID,
		Completed:      true,
	}, nil
}

func normalizeImageUploadRequest(req ImageUploadRequest) (ImageUploadRequest, error) {
	name := strings.TrimSpace(req.FileName)
	if name == "" {
		name = "image.png"
	}
	// Keep only the base name so path segments cannot leak into upstream metadata.
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = "image.png"
	}
	if utf8.RuneCountInString(name) > maxImageNameRunes {
		runes := []rune(name)
		name = string(runes[:maxImageNameRunes])
	}

	mime := strings.ToLower(strings.TrimSpace(req.MimeType))
	if mime == "" {
		mime = mimeFromFileName(name)
	}
	if !allowedImageMime(mime) {
		return ImageUploadRequest{}, &ServiceError{
			Message:    "mime_type must be image/jpeg, image/png, image/webp, or image/gif",
			StatusCode: 400,
		}
	}

	if req.FileSize <= 0 {
		return ImageUploadRequest{}, &ServiceError{Message: "file_size must be positive", StatusCode: 400}
	}
	if req.FileSize > maxImageUploadBytes {
		return ImageUploadRequest{}, &ServiceError{
			Message:    fmt.Sprintf("file_size exceeds %d bytes", maxImageUploadBytes),
			StatusCode: 400,
		}
	}
	if req.Width < 0 || req.Height < 0 {
		return ImageUploadRequest{}, &ServiceError{Message: "width/height must be non-negative", StatusCode: 400}
	}
	if req.Width > 10000 || req.Height > 10000 {
		return ImageUploadRequest{}, &ServiceError{Message: "width/height too large", StatusCode: 400}
	}

	return ImageUploadRequest{
		FileName: name,
		FileSize: req.FileSize,
		MimeType: mime,
		Width:    req.Width,
		Height:   req.Height,
	}, nil
}

func allowedImageMime(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func mimeFromFileName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	default:
		return "image/png"
	}
}

func validUpstreamFileID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 200 {
		return false
	}
	if strings.ContainsAny(id, "/\\?# \t\r\n") {
		return false
	}
	return true
}

func (s *Service) postFilesCreateOnce(token, proxy string, meta ImageUploadRequest) (status int, text string, err error) {
	client := httpclient.New(s.httpOptions, proxy)
	client.Timeout = filesUploadTimeout
	payload := map[string]any{
		"file_name":                       meta.FileName,
		"file_size":                       meta.FileSize,
		"use_case":                        "multimodal",
		"timezone_offset_min":             -480,
		"reset_rate_limits":               false,
		"supports_direct_azure_multipart": true,
		"mime_type":                       meta.MimeType,
	}
	if meta.Width > 0 {
		payload["width"] = meta.Width
	}
	if meta.Height > 0 {
		payload["height"] = meta.Height
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequest(http.MethodPost, s.filesURL(), bytes.NewReader(raw))
	if err != nil {
		return 0, "", err
	}
	req.Header = s.authHeaders(token, map[string]string{
		"content-type": "application/json",
		"accept":       "application/json",
	})
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, filesUploadBodyLimit))
	return resp.StatusCode, string(body), nil
}

func (s *Service) postFileUploadedOnce(token, proxy, fileID string) (status int, text string, err error) {
	client := httpclient.New(s.httpOptions, proxy)
	client.Timeout = filesUploadTimeout
	endpoint := strings.TrimRight(s.filesURL(), "/") + "/" + fileID + "/uploaded"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return 0, "", err
	}
	req.Header = s.authHeaders(token, map[string]string{
		"content-type": "application/json",
		"accept":       "application/json",
	})
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, filesUploadBodyLimit))
	return resp.StatusCode, string(body), nil
}

func (s *Service) postProcessUploadStreamOnce(token, proxy, fileID string) (status int, text string, err error) {
	client := httpclient.New(s.httpOptions, proxy)
	client.Timeout = filesUploadTimeout
	payload, _ := json.Marshal(map[string]any{
		"file_id":             fileID,
		"use_case":            "multimodal",
		"index_for_retrieval": false,
		"file_name":           "image.png",
	})
	endpoint := strings.TrimRight(s.filesURL(), "/") + "/process_upload_stream"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header = s.authHeaders(token, map[string]string{
		"content-type": "application/json",
		"accept":       "application/json",
	})
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, filesUploadBodyLimit))
	return resp.StatusCode, string(body), nil
}

func (s *Service) filesURL() string {
	if s != nil && strings.TrimSpace(s.filesAPIURL) != "" {
		return strings.TrimSpace(s.filesAPIURL)
	}
	return filesURL
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
