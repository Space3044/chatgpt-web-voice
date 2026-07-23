package accounts

// Account is one ChatGPT web access token entry. Values are intentionally
// stored as plain text in the local SQLite database, as required by the
// gateway deployment model.
type Account struct {
	ID          int64   `json:"id,omitempty"`
	Email       string  `json:"email"`
	AccessToken string  `json:"access_token"`
	DeviceID    string  `json:"device_id,omitempty"`
	Proxy       string  `json:"proxy,omitempty"`
	Status      string  `json:"status,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
	InvalidAt   float64 `json:"invalid_at,omitempty"`
	LastUsedAt  string  `json:"last_used_at,omitempty"`
	CreatedAt   string  `json:"created_at,omitempty"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
}

// AccountUpdate contains mutable account fields. Nil secret pointers preserve
// their current values; a pointer to an empty string clears that field.
type AccountUpdate struct {
	Email       string
	AccessToken *string
	DeviceID    string
	Proxy       *string
	Status      string
	Disabled    bool
}

// PoolStats summarizes account availability for the management panel.
type PoolStats struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Disabled  int `json:"disabled"`
}
