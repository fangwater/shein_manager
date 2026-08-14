package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/sheinconsole"
	"shein-api-manager/internal/xlwms"
)

const (
	defaultListen       = "127.0.0.1:18084"
	defaultXLWMSBaseURL = "https://pangutech.online/warehouse-console/api"
)

type config struct {
	Listen         string
	DatabaseURL    string
	DefaultShopKey string
	XLWMSBaseURL   string
	RequestTimeout time.Duration
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("SHEIN Go service stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := requireLoopbackAddress(cfg.Listen); err != nil {
		return err
	}
	store, err := shein.NewStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	xlwmsClient, err := xlwms.NewClient(cfg.XLWMSBaseURL, cfg.RequestTimeout)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	server := &http.Server{
		Handler:           sheinconsole.New(store, cfg.DefaultShopKey, cfg.RequestTimeout, logger, xlwmsClient),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serverErrors := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrors <- serveErr
		}
	}()
	logger.Info("SHEIN Go management service started", "listen", listener.Addr().String(), "endpoints", len(shein.Endpoints))
	select {
	case <-ctx.Done():
		logger.Info("SHEIN Go management service shutdown requested")
	case serveErr := <-serverErrors:
		return serveErr
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func loadConfig() (config, error) {
	requestTimeout := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("SHEIN_GO_REQUEST_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return config{}, errors.New("SHEIN_GO_REQUEST_TIMEOUT must be a positive duration")
		}
		requestTimeout = parsed
	}
	cfg := config{
		Listen:         envOrDefault("SHEIN_GO_LISTEN", defaultListen),
		DatabaseURL:    strings.TrimSpace(os.Getenv("SHEIN_DATABASE_URL")),
		DefaultShopKey: envOrDefault("SHEIN_SHOP_KEY", "default"),
		XLWMSBaseURL:   envOrDefault("XLWMS_BASE_URL", defaultXLWMSBaseURL),
		RequestTimeout: requestTimeout,
	}
	if cfg.DatabaseURL == "" {
		return config{}, errors.New("SHEIN_DATABASE_URL is required")
	}
	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid SHEIN_GO_LISTEN: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("SHEIN_GO_LISTEN must use a loopback address")
	}
	return nil
}
