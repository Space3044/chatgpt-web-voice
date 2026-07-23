package main

import (
	"flag"
	"log/slog"
	"os"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
)

func main() {
	from := flag.String("from", "./data/accounts.json", "legacy accounts JSON file")
	database := flag.String("database", "./data/voice.db", "target SQLite database")
	flag.Parse()

	logger := logging.New("json", "info")
	slog.SetDefault(logger)

	keyRaw := strings.TrimSpace(os.Getenv("VOICE_TOKEN_ENCRYPTION_KEY"))
	if keyRaw == "" {
		logger.Error("account_migration_failed", "stage", "config", "error", "VOICE_TOKEN_ENCRYPTION_KEY is required")
		os.Exit(1)
	}
	key, err := secretbox.ParseKey(keyRaw)
	if err != nil {
		logger.Error("account_migration_failed", "stage", "config", "error", err)
		os.Exit(1)
	}
	box, err := secretbox.New(key)
	if err != nil {
		logger.Error("account_migration_failed", "stage", "config", "error", err)
		os.Exit(1)
	}

	items, err := accounts.ImportJSONFile(*from)
	if err != nil {
		logger.Error("account_migration_failed", "stage", "read", "error", err)
		os.Exit(1)
	}
	pool, err := accounts.NewPool(*database)
	if err != nil {
		logger.Error("account_migration_failed", "stage", "open_database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	pool.WithBox(box)

	for index, account := range items {
		if err := pool.Upsert(account); err != nil {
			logger.Error("account_migration_failed", "stage", "upsert", "account_index", index, "error", err)
			os.Exit(1)
		}
	}
	logger.Info("account_migration_completed", "imported_accounts", len(items), "database", *database)
}
