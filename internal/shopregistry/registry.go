package shopregistry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	codePattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

type Shop struct {
	Code       string
	Name       string
	SchemaName string
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Registry struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Registry, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create SHEIN shop registry pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect SHEIN shop registry database: %w", err)
	}
	registry := &Registry{pool: pool}
	if err := registry.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Close() {
	r.pool.Close()
}

func (r *Registry) migrate(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.shein_shops (
			shop_key text PRIMARY KEY,
			shop_name text,
			schema_name text,
			app_id text,
			open_key_id text NOT NULL,
			secret_key text NOT NULL,
			base_url text NOT NULL,
			state text,
			enabled boolean NOT NULL DEFAULT true,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		ALTER TABLE public.shein_shops ADD COLUMN IF NOT EXISTS shop_name text;
		ALTER TABLE public.shein_shops ADD COLUMN IF NOT EXISTS schema_name text;
		ALTER TABLE public.shein_shops ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
		UPDATE public.shein_shops
		SET shop_name = COALESCE(NULLIF(BTRIM(shop_name), ''), shop_key),
			schema_name = COALESCE(
				NULLIF(BTRIM(schema_name), ''),
				'shein_' || TRIM(BOTH '_' FROM regexp_replace(lower(shop_key), '[^a-z0-9]+', '_', 'g'))
			)
		WHERE NULLIF(BTRIM(shop_name), '') IS NULL OR NULLIF(BTRIM(schema_name), '') IS NULL;
		ALTER TABLE public.shein_shops ALTER COLUMN shop_name SET NOT NULL;
		ALTER TABLE public.shein_shops ALTER COLUMN schema_name SET NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_shein_shops_schema_name
			ON public.shein_shops(schema_name);
	`)
	if err != nil {
		return fmt.Errorf("migrate SHEIN shop registry: %w", err)
	}
	return nil
}

func (r *Registry) List(ctx context.Context) ([]Shop, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT shop_key, shop_name, schema_name, enabled, created_at, updated_at
		FROM public.shein_shops
		ORDER BY shop_name, shop_key
	`)
	if err != nil {
		return nil, fmt.Errorf("list SHEIN shops: %w", err)
	}
	defer rows.Close()
	shops := make([]Shop, 0, 4)
	for rows.Next() {
		var shop Shop
		if err := rows.Scan(&shop.Code, &shop.Name, &shop.SchemaName, &shop.Enabled, &shop.CreatedAt, &shop.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan SHEIN shop: %w", err)
		}
		shop.Code = strings.TrimSpace(shop.Code)
		shop.Name = strings.TrimSpace(shop.Name)
		shop.SchemaName = strings.TrimSpace(shop.SchemaName)
		if !codePattern.MatchString(shop.Code) {
			return nil, fmt.Errorf("invalid SHEIN shop code %q", shop.Code)
		}
		if shop.Name == "" {
			return nil, errors.New("SHEIN shop name is required")
		}
		if !schemaPattern.MatchString(shop.SchemaName) {
			return nil, fmt.Errorf("invalid PostgreSQL schema for SHEIN shop %s", shop.Code)
		}
		shops = append(shops, shop)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list SHEIN shops: %w", err)
	}
	return shops, nil
}
