package accounts

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// ImportJSONFile reads the former accounts.json format for an explicit,
// one-time migration. The server itself never calls this function.
func ImportJSONFile(path string) ([]Account, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read legacy accounts file: %w", err)
	}

	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode legacy accounts file: %w", err)
	}

	var items []any
	switch value := root.(type) {
	case []any:
		items = value
	case map[string]any:
		for _, key := range []string{"accounts", "items"} {
			if candidate, ok := value[key].([]any); ok {
				items = candidate
				break
			}
		}
	}
	if items == nil {
		return nil, fmt.Errorf("legacy accounts file must be a list or {accounts:[...]}")
	}

	accounts := make([]Account, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			continue
		}
		account := Account{
			Email:        stringField(fields, "email"),
			AccessToken:  firstStringField(fields, "access_token", "token"),
			RefreshToken: stringField(fields, "refresh_token"),
			DeviceID:     firstStringField(fields, "device_id", "oai-device-id"),
			Proxy:        stringField(fields, "proxy"),
			Status:       stringField(fields, "status"),
			Disabled:     boolField(fields, "disabled"),
			InvalidAt:    floatField(fields, "invalid_at"),
		}
		if account.Status == "禁用" {
			account.Disabled = true
		}
		if account.AccessToken != "" {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func stringField(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstStringField(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringField(fields, key); value != "" {
			return value
		}
	}
	return ""
}

func boolField(fields map[string]any, key string) bool {
	value, ok := fields[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || typed == "true" || typed == "是"
	default:
		return false
	}
}

func floatField(fields map[string]any, key string) float64 {
	value, ok := fields[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case string:
		parsed, _ := strconv.ParseFloat(typed, 64)
		return parsed
	default:
		return 0
	}
}
