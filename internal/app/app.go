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
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/auth"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/conversations"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
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

	accountPool := accounts.NewPoolFromDB(db)
	conversationStore := conversations.NewStore(db)
	available, err := accountPool.AvailableCount()
	if err != nil {
		return fmt.Errorf("account database check failed: %w", err)
	}
	logger.Info("account_database_ready", "available_accounts", available)

	authManager := auth.New(
		cfg.AuthUsername,
		cfg.AuthPassword,
		time.Duration(cfg.AuthSessionTTL)*time.Second,
		logger,
	)
	voiceService := voice.New(cfg, accountPool, logger)
	apiServer := api.New(api.Dependencies{
		Voice:         voiceService,
		Accounts:      accountPool,
		Conversations: conversationStore,
	})

	handler := newHandler(cfg, authManager, apiServer, logger)
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

func newHandler(cfg config.Config, authManager *auth.Manager, apiServer *api.Server, logger *slog.Logger) http.Handler {
	protected := http.NewServeMux()
	apiServer.Register(protected)
	registerStaticRoutes(protected, cfg.StaticDir)

	root := http.NewServeMux()
	root.Handle("GET /login", authManager.LoginPage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, joinStatic(cfg.StaticDir, "login.html"))
	})))
	root.HandleFunc("POST /api/auth/login", authManager.HandleLogin)
	root.Handle("POST /api/auth/logout", authManager.Require(http.HandlerFunc(authManager.HandleLogout)))
	root.Handle("GET /api/auth/session", authManager.Require(http.HandlerFunc(authManager.HandleSession)))
	root.Handle("/", authManager.Require(protected))

	return logging.HTTPMiddleware(logger, securityHeaders(root))
}
