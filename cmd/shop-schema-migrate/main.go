package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	shopCodePattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	schemaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

var privateTables = []string{
	"shein_orders",
	"shein_return_applications",
	"shein_order_returns",
	"shein_products",
	"shein_product_details",
	"shein_sync_runs",
	"shein_order_full_sync_jobs",
	"shein_order_full_sync_windows",
	"shein_sku_mappings",
	"shein_sku_mapping_settings",
	"shein_warehouse_skus",
	"shein_inventory_records",
	"shein_inventory_export_agent_fees",
	"shein_inventory_ocean_import_fees",
	"shein_inventory_inbound_fees",
	"inventory_schema_migrations",
	"inventory_cost_templates",
	"inventory_tickets",
	"inventory_ticket_lines",
	"inventory_cost_categories",
	"inventory_template_settings",
	"inventory_cost_types",
	"inventory_cost_entries",
	"shein_go_api_operations",
	"shein_go_fulfillment_tasks",
	"shein_go_auto_fulfillment_jobs",
	"shein_go_bulk_fulfillment_batches",
	"shein_go_bulk_fulfillment_items",
	"shein_go_shipping_quotes",
	"shein_go_shipping_quote_candidates",
	"shein_label_purchase_choices",
	"shein_label_purchase_candidates",
}

func main() {
	fromShop := flag.String("from-shop", "default", "current registered shop code")
	shopCode := flag.String("shop", "beauty-hangers-home", "destination shop code")
	shopName := flag.String("name", "Beauty Hangers home", "destination shop display name")
	fromSchema := flag.String("from", "public", "current private-data schema")
	toSchema := flag.String("to", "shein_beauty_hangers_home", "destination private-data schema")
	dryRun := flag.Bool("dry-run", false, "exercise the full migration and roll it back")
	flag.Parse()

	databaseURL := strings.TrimSpace(os.Getenv("SHEIN_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if err := run(
		context.Background(), databaseURL, strings.TrimSpace(*fromShop), strings.TrimSpace(*shopCode),
		strings.TrimSpace(*shopName), strings.TrimSpace(*fromSchema), strings.TrimSpace(*toSchema), *dryRun,
	); err != nil {
		fmt.Fprintln(os.Stderr, "SHEIN shop schema migration failed:", err)
		os.Exit(1)
	}
}

func run(parent context.Context, databaseURL, fromShop, shopCode, shopName, fromSchema, toSchema string, dryRun bool) error {
	if databaseURL == "" {
		return errors.New("SHEIN_DATABASE_URL or DATABASE_URL is required")
	}
	if !shopCodePattern.MatchString(fromShop) || !shopCodePattern.MatchString(shopCode) {
		return errors.New("shop codes must use lowercase letters, digits, and hyphens")
	}
	if shopName == "" {
		return errors.New("shop name is required")
	}
	if !schemaNamePattern.MatchString(fromSchema) || !schemaNamePattern.MatchString(toSchema) || fromSchema == toSchema {
		return errors.New("from and to must be different valid PostgreSQL schema names")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database configuration: %w", err)
	}
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('shein:shop-schema-migration'))`); err != nil {
		return fmt.Errorf("lock migration: %w", err)
	}
	if err := ensureRegistryColumns(ctx, tx); err != nil {
		return err
	}
	var existingSchema string
	err = tx.QueryRow(ctx, `SELECT schema_name FROM public.shein_shops WHERE shop_key=$1 FOR UPDATE`, shopCode).Scan(&existingSchema)
	if err == nil && existingSchema == toSchema {
		fmt.Printf("shop %s already uses schema %s\n", shopCode, toSchema)
		if dryRun {
			return tx.Rollback(ctx)
		}
		return tx.Commit(ctx)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect destination shop: %w", err)
	}
	if fromShop != shopCode && err == nil {
		return fmt.Errorf("destination shop %s already exists with schema %s", shopCode, existingSchema)
	}
	var sourceSchema string
	if err := tx.QueryRow(ctx, `SELECT schema_name FROM public.shein_shops WHERE shop_key=$1 FOR UPDATE`, fromShop).Scan(&sourceSchema); err != nil {
		return fmt.Errorf("load source shop registry: %w", err)
	}
	if sourceSchema != fromSchema && sourceSchema != "" {
		return fmt.Errorf("source shop registry uses %s, expected %s", sourceSchema, fromSchema)
	}
	var targetRelations int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relkind IN ('r','p','S','v','m','f')
	`, toSchema).Scan(&targetRelations); err != nil {
		return fmt.Errorf("inspect target schema: %w", err)
	}
	if targetRelations != 0 {
		return fmt.Errorf("target schema %s is not empty", toSchema)
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{toSchema}.Sanitize()); err != nil {
		return fmt.Errorf("create target schema: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL search_path TO `+pgx.Identifier{fromSchema}.Sanitize()+`, public`); err != nil {
		return fmt.Errorf("set migration search path: %w", err)
	}
	if err := validateSourceTables(ctx, tx, fromSchema); err != nil {
		return err
	}
	if err := enableForeignKeyUpdates(ctx, tx, fromSchema); err != nil {
		return err
	}
	tableCounts := make(map[string]int64, len(privateTables))
	for _, table := range privateTables {
		var count int64
		statement := `SELECT count(*) FROM ` + pgx.Identifier{fromSchema, table}.Sanitize()
		if err := tx.QueryRow(ctx, statement).Scan(&count); err != nil {
			return fmt.Errorf("count %s before migration: %w", table, err)
		}
		tableCounts[table] = count
	}
	if fromShop != shopCode {
		command, err := tx.Exec(ctx, `
			UPDATE public.shein_shops
			SET shop_key=$1, shop_name=$2, schema_name=$3,
				state=CASE WHEN state=$4 THEN $1 ELSE state END, updated_at=now()
			WHERE shop_key=$4
		`, shopCode, shopName, toSchema, fromShop)
		if err != nil {
			return fmt.Errorf("rename shop registry: %w", err)
		}
		if command.RowsAffected() != 1 {
			return errors.New("shop registry was not renamed")
		}
		if err := updateShopKeys(ctx, tx, fromSchema, fromShop, shopCode); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE public.shein_shops SET shop_name=$1, schema_name=$2, updated_at=now() WHERE shop_key=$3
	`, shopName, toSchema, shopCode); err != nil {
		return fmt.Errorf("update shop registry: %w", err)
	}
	for _, table := range privateTables {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, fromSchema+"."+table).Scan(&exists); err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("required private table %s.%s is missing", fromSchema, table)
		}
		statement := `ALTER TABLE ` + pgx.Identifier{fromSchema, table}.Sanitize() + ` SET SCHEMA ` + pgx.Identifier{toSchema}.Sanitize()
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("move %s: %w", table, err)
		}
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{toSchema, table}.Sanitize()).Scan(&count); err != nil {
			return fmt.Errorf("count %s after migration: %w", table, err)
		}
		if count != tableCounts[table] {
			return fmt.Errorf("row count changed for %s: before=%d after=%d", table, tableCounts[table], count)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE public.shein_shops
		SET shop_name=$1, schema_name=$2, enabled=true, updated_at=now()
		WHERE shop_key=$3
	`, shopName, toSchema, shopCode); err != nil {
		return fmt.Errorf("finalize shop registry: %w", err)
	}
	if err := verifyMigration(ctx, tx, fromShop, shopCode, shopName, fromSchema, toSchema); err != nil {
		return err
	}
	if dryRun {
		if err := tx.Rollback(ctx); err != nil {
			return fmt.Errorf("roll back dry run: %w", err)
		}
		fmt.Printf("dry run verified %d private tables for %s and rolled back\n", len(privateTables), shopCode)
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	fmt.Printf("moved %d private tables for %s to schema %s\n", len(privateTables), shopCode, toSchema)
	return nil
}

func validateSourceTables(ctx context.Context, tx pgx.Tx, schemaName string) error {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1
		  AND c.relkind IN ('r','p','v','m','f')
		  AND (c.relname LIKE 'shein!_%' ESCAPE '!' OR c.relname LIKE 'inventory!_%' ESCAPE '!')
		  AND c.relname <> 'shein_shops'
		  AND NOT (c.relname=ANY($2))
		ORDER BY c.relname
	`, schemaName, privateTables)
	if err != nil {
		return fmt.Errorf("inspect source tables: %w", err)
	}
	defer rows.Close()
	var unexpected []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return fmt.Errorf("scan unexpected source table: %w", err)
		}
		unexpected = append(unexpected, table)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate source tables: %w", err)
	}
	if len(unexpected) != 0 {
		return fmt.Errorf("private tables are missing from the migration manifest: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func verifyMigration(ctx context.Context, tx pgx.Tx, fromShop, shopCode, shopName, fromSchema, toSchema string) error {
	var registryMatches bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.shein_shops
			WHERE shop_key=$1 AND shop_name=$2 AND schema_name=$3 AND enabled=true
		)
	`, shopCode, shopName, toSchema).Scan(&registryMatches); err != nil {
		return fmt.Errorf("verify shop registry: %w", err)
	}
	if !registryMatches {
		return errors.New("destination shop registry does not match the requested identity")
	}
	if fromShop != shopCode {
		var sourceExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.shein_shops WHERE shop_key=$1)`, fromShop).Scan(&sourceExists); err != nil {
			return fmt.Errorf("verify source shop removal: %w", err)
		}
		if sourceExists {
			return fmt.Errorf("source shop %s still exists", fromShop)
		}
	}
	for _, table := range privateTables {
		var sourceExists, destinationExists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL, to_regclass($2) IS NOT NULL`,
			fromSchema+"."+table, toSchema+"."+table,
		).Scan(&sourceExists, &destinationExists); err != nil {
			return fmt.Errorf("verify relation %s: %w", table, err)
		}
		if sourceExists || !destinationExists {
			return fmt.Errorf("private relation %s was not isolated in schema %s", table, toSchema)
		}
		var hasShopKey bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM information_schema.columns
				WHERE table_schema=$1 AND table_name=$2 AND column_name='shop_key'
			)
		`, toSchema, table).Scan(&hasShopKey); err != nil {
			return fmt.Errorf("inspect destination shop key on %s: %w", table, err)
		}
		if !hasShopKey {
			continue
		}
		var foreignRows int64
		statement := `SELECT count(*) FROM ` + pgx.Identifier{toSchema, table}.Sanitize() + ` WHERE shop_key <> $1 OR shop_key IS NULL`
		if err := tx.QueryRow(ctx, statement, shopCode).Scan(&foreignRows); err != nil {
			return fmt.Errorf("verify destination shop key in %s: %w", table, err)
		}
		if foreignRows != 0 {
			return fmt.Errorf("table %s contains %d rows outside shop %s", table, foreignRows, shopCode)
		}
	}
	return nil
}

func ensureRegistryColumns(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		ALTER TABLE public.shein_shops ADD COLUMN IF NOT EXISTS shop_name text;
		ALTER TABLE public.shein_shops ADD COLUMN IF NOT EXISTS schema_name text;
		ALTER TABLE public.shein_shops ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
		UPDATE public.shein_shops
		SET shop_name=COALESCE(NULLIF(BTRIM(shop_name), ''), shop_key),
			schema_name=COALESCE(NULLIF(BTRIM(schema_name), ''), 'public');
		CREATE UNIQUE INDEX IF NOT EXISTS idx_shein_shops_schema_name ON public.shein_shops(schema_name);
	`)
	if err != nil {
		return fmt.Errorf("prepare shop registry: %w", err)
	}
	return nil
}

func enableForeignKeyUpdates(ctx context.Context, tx pgx.Tx, schemaName string) error {
	rows, err := tx.Query(ctx, `
		SELECT c.relname, con.conname, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid=con.conrelid
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE con.contype='f' AND n.nspname=$1 AND c.relname=ANY($2)
		ORDER BY c.relname, con.conname
	`, schemaName, privateTables)
	if err != nil {
		return fmt.Errorf("list foreign keys: %w", err)
	}
	type foreignKey struct{ table, name, definition string }
	var constraints []foreignKey
	for rows.Next() {
		var item foreignKey
		if err := rows.Scan(&item.table, &item.name, &item.definition); err != nil {
			rows.Close()
			return fmt.Errorf("scan foreign key: %w", err)
		}
		constraints = append(constraints, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate foreign keys: %w", err)
	}
	for _, item := range constraints {
		if strings.Contains(item.definition, "ON UPDATE") {
			continue
		}
		definition := item.definition
		if index := strings.Index(definition, " ON DELETE "); index >= 0 {
			definition = definition[:index] + " ON UPDATE CASCADE" + definition[index:]
		} else {
			definition += " ON UPDATE CASCADE"
		}
		table := pgx.Identifier{schemaName, item.table}.Sanitize()
		constraint := pgx.Identifier{item.name}.Sanitize()
		if _, err := tx.Exec(ctx, `ALTER TABLE `+table+` DROP CONSTRAINT `+constraint); err != nil {
			return fmt.Errorf("drop foreign key %s: %w", item.name, err)
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE `+table+` ADD CONSTRAINT `+constraint+` `+definition); err != nil {
			return fmt.Errorf("recreate foreign key %s: %w", item.name, err)
		}
	}
	return nil
}

func updateShopKeys(ctx context.Context, tx pgx.Tx, schemaName, fromShop, shopCode string) error {
	ordered := []string{"shein_go_shipping_quotes"}
	seen := map[string]bool{"shein_go_shipping_quotes": true}
	for _, table := range privateTables {
		if !seen[table] {
			ordered = append(ordered, table)
		}
	}
	for _, table := range ordered {
		var hasShopKey bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM information_schema.columns
				WHERE table_schema=$1 AND table_name=$2 AND column_name='shop_key'
			)
		`, schemaName, table).Scan(&hasShopKey); err != nil {
			return fmt.Errorf("inspect shop key on %s: %w", table, err)
		}
		if !hasShopKey {
			continue
		}
		statement := `UPDATE ` + pgx.Identifier{schemaName, table}.Sanitize() + ` SET shop_key=$1 WHERE shop_key=$2`
		if _, err := tx.Exec(ctx, statement, shopCode, fromShop); err != nil {
			return fmt.Errorf("rename shop key in %s: %w", table, err)
		}
	}
	return nil
}
