package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/api"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/auth"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/callsessions"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/conversations"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/voice"
)

// Run loads configuration, wires dependencies, and serves until interrupted.
func Run() error {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	logger := logging.New(cfg.LogFormat, cfg.LogLevel)
	slog.SetDefault(logger)
	logger.Info("configuration_loaded",
		"environment", cfg.Environment,
		"listen_addr", cfg.ListenAddr,
		"database_file", cfg.DatabaseFile,
		"tls", cfg.TLS,
		"upstream_skip_ssl_verify", cfg.SkipSSLVerify,
		"log_format", cfg.LogFormat,
		"log_level", cfg.LogLevel,
	)

	db, err := store.Open(cfg.DatabaseFile)
	if err != nil {
		return fmt.Errorf("account database open failed: %w", err)
	}
	defer db.Close()

	tokenKey, err := secretbox.ParseKey(cfg.TokenEncryptionKey)
	if err != nil {
		return fmt.Errorf("invalid VOICE_TOKEN_ENCRYPTION_KEY: %w", err)
	}
	tokenBox, err := secretbox.New(tokenKey)
	if err != nil {
		return fmt.Errorf("token encryption setup failed: %w", err)
	}

	accountPool := accounts.NewPoolFromDB(db).WithBox(tokenBox)
	rewritten, err := accountPool.SealStoredTokens()
	if err != nil {
		return fmt.Errorf("seal stored access tokens failed: %w", err)
	}
	if rewritten > 0 {
		logger.Info("access_tokens_sealed", "rewritten_accounts", rewritten)
	}

	conversationStore := conversations.NewStore(db)
	callSessionStore := callsessions.NewStore(db)
	apiKeyStore := apikeys.NewStore(db)
	releasedOrphans, err := callSessionStore.MarkAllActiveReleased()
	if err != nil {
		return fmt.Errorf("release orphan call sessions failed: %w", err)
	}
	if releasedOrphans > 0 {
		logger.Info("call_sessions_released_on_startup", "count", releasedOrphans)
	}
	available, err := accountPool.AvailableCount()
	if err != nil {
		return fmt.Errorf("account database check failed: %w", err)
	}
	logger.Info("account_database_ready", "available_accounts", available)

	authManager := auth.NewWithLimits(
		cfg.AuthUsername,
		cfg.AuthPassword,
		time.Duration(cfg.AuthSessionTTL)*time.Second,
		auth.Limits{
			MaxFailures:  cfg.LoginMaxFailures,
			Window:       time.Duration(cfg.LoginWindowSeconds) * time.Second,
			Lockout:      time.Duration(cfg.LoginLockoutSeconds) * time.Second,
			FailureDelay: 200 * time.Millisecond,
		},
		logger,
	).WithSessionStore(auth.NewSessionStore(db))
	apiKeyManager := auth.NewAPIKeyManager(apiKeyStore, logger)
	voiceService := voice.New(cfg, accountPool, logger).WithCallSessions(callSessionStore)
	apiServer := api.New(api.Dependencies{
		Voice:         voiceService,
		Accounts:      accountPool,
		Conversations: conversationStore,
		APIKeys:       apiKeyStore,
		CallSessions:  callSessionStore,
	})

	handler := newHandler(cfg, authManager, apiKeyManager, apiServer, logger)
	server := &http.Server{
		Addr:              normalizeListenAddr(cfg.ListenAddr),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	certFile, keyFile, scheme, err := prepareTLS(cfg, logger)
	if err != nil {
		return fmt.Errorf("tls setup failed: %w", err)
	}
	logListeningAddresses(server.Addr, cfg.StaticDir, scheme, logger)

	serverErr := make(chan error, 1)
	go func() {
		if cfg.TLS {
			serverErr <- server.ListenAndServeTLS(certFile, keyFile)
			return
		}
		serverErr <- server.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	select {
	case sig := <-stop:
		logger.Info("shutdown_requested", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown failed: %w", err)
		}
		logger.Info("shutdown_completed")
		return nil
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	}
}

func newHandler(cfg config.Config, authManager *auth.Manager, apiKeyManager *auth.APIKeyManager, apiServer *api.Server, logger *slog.Logger) http.Handler {
	protected := http.NewServeMux()
	apiServer.Register(protected)
	registerStaticRoutes(protected, cfg.StaticDir)
	downstream := http.NewServeMux()
	apiServer.RegisterDownstream(downstream)

	root := http.NewServeMux()
	// Shared CSS/assets must be public: login.html loads /static/app.css before auth.
	registerPublicStaticAssets(root, cfg.StaticDir)
	root.Handle("GET /login", authManager.LoginPage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, joinStatic(cfg.StaticDir, "login.html"))
	})))
	root.HandleFunc("POST /api/auth/login", authManager.HandleLogin)
	root.Handle("POST /api/auth/logout", authManager.Require(http.HandlerFunc(authManager.HandleLogout)))
	root.Handle("GET /api/auth/session", authManager.Require(http.HandlerFunc(authManager.HandleSession)))
	root.Handle("/v1/", apiKeyManager.Require(downstream))
	root.Handle("/", authManager.Require(protected))

	return logging.HTTPMiddleware(logger, securityHeaders(root))
}
