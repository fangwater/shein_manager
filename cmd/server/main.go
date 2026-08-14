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
	"shein-api-manager/internal/shopregistry"
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
	registry, err := shopregistry.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer registry.Close()
	shops, err := registry.List(ctx)
	if err != nil {
		return err
	}
	xlwmsClient, err := xlwms.NewClient(cfg.XLWMSBaseURL, cfg.RequestTimeout)
	if err != nil {
		return err
	}
	handlers := make(map[string]http.Handler, len(shops))
	shopInfo := make([]sheinconsole.ShopInfo, 0, len(shops))
	for _, shop := range shops {
		if !shop.Enabled {
			continue
		}
		destination, destinationErr := shein.NewStoreForShop(ctx, cfg.DatabaseURL, shop.SchemaName, shop.Code)
		if destinationErr != nil {
			return fmt.Errorf("initialize SHEIN shop %s database: %w", shop.Code, destinationErr)
		}
		defer destination.Close()
		if err := destination.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate SHEIN shop %s: %w", shop.Code, err)
		}
		shopLogger := logger.With("shop_code", shop.Code, "shop_name", shop.Name)
		handlers[shop.Code] = sheinconsole.New(
			destination, shop.Code, shop.Name, cfg.RequestTimeout, shopLogger, xlwmsClient,
		)
		shopInfo = append(shopInfo, sheinconsole.ShopInfo{
			Code: shop.Code, Name: shop.Name, Default: shop.Code == cfg.DefaultShopKey,
		})
	}
	if _, ok := handlers[cfg.DefaultShopKey]; !ok {
		return errors.New("default SHEIN shop is not enabled")
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	server := &http.Server{
		Handler:           sheinconsole.NewShopRouter(cfg.DefaultShopKey, shopInfo, handlers),
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
	logger.Info("SHEIN Go management service started", "listen", listener.Addr().String(), "default_shop", cfg.DefaultShopKey, "shop_count", len(handlers), "endpoints", len(shein.Endpoints))
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
		DefaultShopKey: envOrDefault("SHEIN_SHOP_KEY", "beauty-hangers-home"),
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
