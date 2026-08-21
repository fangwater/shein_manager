package shein

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LocalOrder struct {
	OrderNo               string         `json:"order_no"`
	OrderStatus           string         `json:"order_status"`
	OrderStatusLabel      string         `json:"order_status_label,omitempty"`
	OrderStatusNormalized string         `json:"order_status_normalized"`
	OrderType             string         `json:"order_type,omitempty"`
	OrderTypeLabel        string         `json:"order_type_label,omitempty"`
	OrderCreatedAt        string         `json:"order_created_at,omitempty"`
	OrderUpdatedAt        string         `json:"order_updated_at,omitempty"`
	ReturnDetected        bool           `json:"return_detected"`
	ReturnStatus          string         `json:"return_status"`
	OrderReturnTime       string         `json:"order_return_time,omitempty"`
	ListPayload           map[string]any `json:"list_payload"`
	DetailPayload         map[string]any `json:"detail_payload,omitempty"`
	DetailFetchedAt       *time.Time     `json:"detail_fetched_at,omitempty"`
	FirstSeenAt           time.Time      `json:"first_seen_at"`
	LastSeenAt            time.Time      `json:"last_seen_at"`
}

type OrderSyncStatus struct {
	ID            int64      `json:"id"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	FetchedOrders int        `json:"fetched_orders"`
	ErrorMessage  string     `json:"error_message,omitempty"`
}

func (s *Store) ListOrders(ctx context.Context, shopKey, query string, history bool, page, pageSize int) ([]LocalOrder, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	shopKey = strings.TrimSpace(shopKey)
	where := `WHERE shop_key = $1 AND ` + orderHistoryPredicate(history)
	args := []any{shopKey}
	if query = strings.TrimSpace(query); query != "" {
		args = append(args, "%"+query+"%")
		where += fmt.Sprintf(` AND (order_no ILIKE $%d OR list_payload::text ILIKE $%d OR COALESCE(detail_payload, '{}'::jsonb)::text ILIKE $%d)`, len(args), len(args), len(args))
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM shein_orders `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count SHEIN orders: %w", err)
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT order_no, COALESCE(order_status, ''), COALESCE(order_status_label, ''),
		       COALESCE(order_status_normalized, 'unknown'), COALESCE(order_type, ''),
		       COALESCE(order_type_label, ''), COALESCE(order_created_at, ''),
		       COALESCE(order_updated_at, ''), return_detected, COALESCE(return_status, 'none'),
		       COALESCE(order_return_time, ''), list_payload,
		       'null'::jsonb, detail_fetched_at, first_seen_at, last_seen_at
		FROM shein_orders %s
		ORDER BY last_seen_at DESC, order_no
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list SHEIN orders: %w", err)
	}
	defer rows.Close()
	items := make([]LocalOrder, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanLocalOrder(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate SHEIN orders: %w", err)
	}
	return items, total, nil
}

func (s *Store) GetOrder(ctx context.Context, shopKey, orderNo string) (LocalOrder, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT order_no, COALESCE(order_status, ''), COALESCE(order_status_label, ''),
		       COALESCE(order_status_normalized, 'unknown'), COALESCE(order_type, ''),
		       COALESCE(order_type_label, ''), COALESCE(order_created_at, ''),
		       COALESCE(order_updated_at, ''), return_detected, COALESCE(return_status, 'none'),
		       COALESCE(order_return_time, ''), list_payload,
		       COALESCE(detail_payload, 'null'::jsonb), detail_fetched_at, first_seen_at, last_seen_at
		FROM shein_orders WHERE shop_key = $1 AND order_no = $2
	`, strings.TrimSpace(shopKey), strings.TrimSpace(orderNo))
	item, err := scanLocalOrder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return LocalOrder{}, ErrOrderNotFound
	}
	return item, err
}

func (s *Store) OrderDetailCandidates(ctx context.Context, shopKey string, limit int) ([]string, error) {
	if limit < 1 || limit > MaxOrderDetailBatch {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT order_no FROM shein_orders
		WHERE shop_key = $1
		  AND (order_status IN ('1', '2') OR order_status_normalized IN ('pending_processing', 'pending_shipping'))
		  AND (detail_payload IS NULL OR detail_fetched_at IS NULL OR detail_fetched_at < last_seen_at)
		ORDER BY detail_fetched_at NULLS FIRST, last_seen_at DESC
		LIMIT $2
	`, strings.TrimSpace(shopKey), limit)
	if err != nil {
		return nil, fmt.Errorf("list SHEIN order detail candidates: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan SHEIN order detail candidate: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) StartOrderSync(ctx context.Context, shopKey string) (OrderSyncStatus, error) {
	var status OrderSyncStatus
	err := s.pool.QueryRow(ctx, `
		INSERT INTO shein_go_order_sync_runs(shop_key, status)
		VALUES ($1, 'running') RETURNING id, status, started_at
	`, strings.TrimSpace(shopKey)).Scan(&status.ID, &status.Status, &status.StartedAt)
	return status, err
}

func (s *Store) FinishOrderSync(ctx context.Context, id int64, status string, fetched int, message string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE shein_go_order_sync_runs
		SET status = $2, completed_at = now(), fetched_orders = $3, error_message = $4
		WHERE id = $1
	`, id, status, fetched, message)
	return err
}

func (s *Store) LatestOrderSync(ctx context.Context, shopKey string) (OrderSyncStatus, error) {
	var status OrderSyncStatus
	err := s.pool.QueryRow(ctx, `
		SELECT id, status, started_at, completed_at, fetched_orders, COALESCE(error_message, '')
		FROM shein_go_order_sync_runs WHERE shop_key = $1 ORDER BY id DESC LIMIT 1
	`, strings.TrimSpace(shopKey)).Scan(&status.ID, &status.Status, &status.StartedAt, &status.CompletedAt, &status.FetchedOrders, &status.ErrorMessage)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderSyncStatus{}, nil
	}
	return status, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLocalOrder(row rowScanner) (LocalOrder, error) {
	var item LocalOrder
	var listJSON, detailJSON []byte
	err := row.Scan(
		&item.OrderNo, &item.OrderStatus, &item.OrderStatusLabel, &item.OrderStatusNormalized,
		&item.OrderType, &item.OrderTypeLabel, &item.OrderCreatedAt, &item.OrderUpdatedAt,
		&item.ReturnDetected, &item.ReturnStatus, &item.OrderReturnTime, &listJSON, &detailJSON,
		&item.DetailFetchedAt, &item.FirstSeenAt, &item.LastSeenAt,
	)
	if err != nil {
		return LocalOrder{}, err
	}
	if err := json.Unmarshal(listJSON, &item.ListPayload); err != nil {
		return LocalOrder{}, fmt.Errorf("decode SHEIN order list payload: %w", err)
	}
	if string(detailJSON) != "null" {
		if err := json.Unmarshal(detailJSON, &item.DetailPayload); err != nil {
			return LocalOrder{}, fmt.Errorf("decode SHEIN order detail payload: %w", err)
		}
	}
	return item, nil
}

func orderHistoryPredicate(history bool) string {
	open := `(order_status IN ('1', '2') OR order_status_normalized IN ('pending_processing', 'pending_shipping'))`
	if history {
		return `NOT ` + open
	}
	return open
}
