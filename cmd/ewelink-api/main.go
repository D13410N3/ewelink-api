package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ewelink-api/internal/bridge"
)

func main() {
	mcpURL := os.Getenv("EWELINK_MCP_URL")
	if mcpURL == "" {
		log.Fatal("EWELINK_MCP_URL is required")
	}

	listenAddr := envOrDefault("LISTEN_ADDR", ":8080")
	refreshInterval, err := time.ParseDuration(envOrDefault("REFRESH_INTERVAL", "1m"))
	if err != nil || refreshInterval <= 0 {
		log.Fatalf("REFRESH_INTERVAL must be a positive Go duration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cache, err := newCache(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := cache.Close(); err != nil {
			log.Printf("close cache: %v", err)
		}
	}()

	metrics, err := bridge.NewMetrics(nil)
	if err != nil {
		log.Fatalf("register metrics: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	service := bridge.NewServiceWithCache(bridge.NewMCPClient(mcpURL, nil), cache, metrics, logger)

	if err := service.RefreshIfStale(ctx, refreshInterval); err != nil {
		log.Fatalf("initial device refresh failed: %v", err)
	}
	go service.RefreshLoop(ctx, refreshInterval)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           bridge.NewHTTPHandler(service),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("eWeLink API bridge listening", "address", listenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
	}
}

func newCache(ctx context.Context) (bridge.Cache, error) {
	switch strings.ToLower(envOrDefault("CACHE_MODE", "memory")) {
	case "memory":
		return bridge.NewMemoryCache(), nil
	case "redis":
		port, err := strconv.Atoi(envOrDefault("REDIS_PORT", "6379"))
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("REDIS_PORT must be between 1 and 65535: %v", err)
		}
		db, err := strconv.Atoi(envOrDefault("REDIS_DB", "0"))
		if err != nil || db < 0 {
			return nil, fmt.Errorf("REDIS_DB must be a non-negative integer: %v", err)
		}
		return bridge.NewRedisCache(ctx, bridge.RedisOptions{
			Host: envOrDefault("REDIS_HOST", "127.0.0.1"),
			Port: port,
			DB:   db,
			Auth: os.Getenv("REDIS_AUTH"),
		})
	default:
		return nil, fmt.Errorf("CACHE_MODE must be memory or redis")
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
