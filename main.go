package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/anthropic"
	"github.com/HeyJiqingCode/CopilotGateway/auth"
	"github.com/HeyJiqingCode/CopilotGateway/internal/copilot"
	"github.com/HeyJiqingCode/CopilotGateway/internal/models"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
	"github.com/HeyJiqingCode/CopilotGateway/proxy"
	"github.com/HeyJiqingCode/CopilotGateway/web"
)

var version = "dev"

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Getenv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	case errors.Is(err, errReported):
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	if cfg.showVersion {
		fmt.Printf("copilotgateway version %s\n", version)
		os.Exit(0)
	}

	// 允许通过环境变量覆盖伪装的客户端版本头(缺省回退到内置真实值)
	copilot.ConfigureFromEnv()

	// 强制鉴权:必须配置至少一个 API key,否则拒绝启动(避免公网开放代理)
	apiKeys := auth.ParseAPIKeys(cfg.apiKey)
	if len(apiKeys) == 0 {
		fmt.Fprintln(os.Stderr, "error: an API key is required. Set COPILOTGW_API_KEY (or -api-key), comma-separated for multiple.")
		os.Exit(1)
	}

	// Set up logging
	logLevel := slog.LevelInfo
	if cfg.debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	// Determine token directory
	tokenDir := cfg.tokenDir
	if tokenDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			slog.Error("failed to get home directory", "error", err)
			os.Exit(1)
		}
		tokenDir = filepath.Join(homeDir, ".config", "copilotgateway")
	}

	// Initialize auth client
	authClient, err := auth.NewClient(tokenDir)
	if err != nil {
		slog.Error("failed to initialize auth client", "error", err)
		os.Exit(1)
	}

	// Get the first Copilot token without blocking on sign-in. Without usable
	// GitHub authorization the device-flow prompt is printed and the server
	// still starts, answering API requests with a 403 carrying the same code.
	ctx := context.Background()
	authClient.Start(ctx, os.Stdout)

	// One upstream client for every handler. Its transport negotiates HTTP/2,
	// so concurrent streams share a Copilot connection.
	upstreamClient := upstream.NewClient(authClient, upstream.NewTransport())

	// Shared models cache — a single fetch populates both raw JSON (for
	// proxying GET /v1/models) and parsed model info (for capability detection).
	modelsCache := models.NewCache(upstreamClient, 5*time.Minute)

	proxyHandler := proxy.NewHandler(upstreamClient, modelsCache)
	proxyHandler.DisableResponsesIDCompat = !cfg.responsesIDCompat
	slog.Info("Responses output ID compatibility", "enabled", cfg.responsesIDCompat)
	proxyHandler.AutoReviewModel = cfg.autoReviewModel
	if cfg.autoReviewModel != "" {
		slog.Info("auto-review model configured", "model", cfg.autoReviewModel)
	}

	anthropicHandler := anthropic.NewHandler(upstreamClient, modelsCache)

	// Set up routes
	mux := http.NewServeMux()

	// Core routes
	mux.Handle("/v1/chat/completions", proxyHandler)
	mux.Handle("/v1/models", proxyHandler)
	mux.Handle("/v1/responses", proxyHandler)
	mux.Handle("/v1/messages", anthropicHandler)

	// 健康检查端点 — 免鉴权,供云端负载均衡器/k8s 探针使用
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Web UI:仅当同时配置了用户名和密码时才注册路由,
	// 未配置时这些路径落到 mux 的 404,不暴露任何页面。
	if webCfg, ok := web.ConfigFromEnv(); ok {
		webCfg.APIKeys = apiKeys
		web.NewHandler(webCfg, modelsCache).Register(mux)
		slog.Info("web ui enabled", "session_hours", webCfg.SessionHours)
	}

	// Pre-warm models cache to avoid cold-cache latency on first request
	go func() {
		slog.Debug("warming models cache")
		modelsCache.Warm(ctx)
	}()

	// Create server
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.host, cfg.port),
		ReadHeaderTimeout: 10 * time.Second,
		// No ReadTimeout — ReadHeaderTimeout protects against slowloris.
		// ReadTimeout would kill long-lived SSE streaming connections.
		IdleTimeout: 120 * time.Second,
		// 中间件链:最外层记录所有请求(含 401),再做 API key 鉴权,最后进 mux
		Handler: logAllRequests(auth.APIKeyAuth(apiKeys, mux)),
	}

	// Start server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("starting server", "host", cfg.host, "port", cfg.port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for interrupt signal or server error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case err := <-serverErr:
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}

	slog.Info("shutting down server")

	// Give the server 30 seconds to finish handling existing requests
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped")
}

func logAllRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("incoming request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
		next.ServeHTTP(w, r)
	})
}
