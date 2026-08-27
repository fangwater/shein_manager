package shein

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type BulkFulfillmentBatch struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	TotalOrders     int        `json:"total_orders"`
	SucceededOrders int        `json:"succeeded_orders"`
	FailedOrders    int        `json:"failed_orders"`
	CurrentOrderNo  string     `json:"current_order_no,omitempty"`
	FailedOrderNo   string     `json:"failed_order_no,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

type BulkFulfillmentItem struct {
	BatchID   string    `json:"batch_id"`
	Sequence  int       `json:"sequence_no"`
	OrderNo   string    `json:"order_no"`
	Status    string    `json:"status"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

const bulkBatchSelect = `SELECT b.id, b.status, b.total_orders,
	b.succeeded_orders, b.failed_orders,
	COALESCE((SELECT i.order_no FROM shein_go_bulk_fulfillment_items i
		WHERE i.batch_id = b.id AND i.status IN ('running', 'pending')
		ORDER BY i.sequence_no LIMIT 1), ''),
	b.failed_order_no, b.last_error, b.created_at, b.updated_at, b.completed_at
	FROM shein_go_bulk_fulfillment_batches b`

func (s *Store) CreateBulkFulfillmentBatch(ctx context.Context, shopKey, batchID string, orderNos []string) (BulkFulfillmentBatch, bool, error) {
	if len(orderNos) == 0 {
		return BulkFulfillmentBatch{}, false, errors.New("automatic fulfillment batch requires orders")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BulkFulfillmentBatch{}, false, fmt.Errorf("begin SHEIN automatic fulfillment batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanBulkBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.shop_key = $1 AND b.status = 'running' ORDER BY b.created_at DESC LIMIT 1`, shopKey))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BulkFulfillmentBatch{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO shein_go_bulk_fulfillment_batches (id, shop_key, total_orders)
		VALUES ($1, $2, $3)
	`, batchID, shopKey, len(orderNos)); err != nil {
		return BulkFulfillmentBatch{}, false, fmt.Errorf("create SHEIN automatic fulfillment batch: %w", err)
	}
	seen := make(map[string]bool)
	sequence := 0
	for _, rawOrderNo := range orderNos {
		orderNo := strings.TrimSpace(rawOrderNo)
		if orderNo == "" || seen[orderNo] {
			continue
		}
		seen[orderNo] = true
		sequence++
		if _, err := tx.Exec(ctx, `
			INSERT INTO shein_go_bulk_fulfillment_items (
				batch_id, sequence_no, order_no
			) VALUES ($1, $2, $3)
		`, batchID, sequence, orderNo); err != nil {
			return BulkFulfillmentBatch{}, false, fmt.Errorf("create SHEIN automatic fulfillment batch item: %w", err)
		}
	}
	if sequence != len(orderNos) {
		return BulkFulfillmentBatch{}, false, errors.New("automatic fulfillment batch contains duplicate or empty orders")
	}
	batch, err := scanBulkBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.id = $1`, batchID))
	if err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkFulfillmentBatch{}, false, fmt.Errorf("commit SHEIN automatic fulfillment batch: %w", err)
	}
	return batch, true, nil
}

func (s *Store) LatestBulkFulfillmentBatch(ctx context.Context, shopKey string) (BulkFulfillmentBatch, error) {
	batch, err := scanBulkBatch(s.pool.QueryRow(ctx, bulkBatchSelect+`
		WHERE b.shop_key = $1 ORDER BY b.created_at DESC LIMIT 1
	`, shopKey))
	if err != nil {
		return BulkFulfillmentBatch{}, err
	}
	return batch, nil
}

func (s *Store) StartNextBulkFulfillmentItem(ctx context.Context, shopKey string) (BulkFulfillmentItem, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BulkFulfillmentItem{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var batchID string
	err = tx.QueryRow(ctx, `
		SELECT id FROM shein_go_bulk_fulfillment_batches
		WHERE shop_key = $1 AND status = 'running'
		ORDER BY created_at DESC LIMIT 1 FOR UPDATE
	`, shopKey).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BulkFulfillmentItem{}, false, nil
	}
	if err != nil {
		return BulkFulfillmentItem{}, false, err
	}
	item, err := scanBulkItem(tx.QueryRow(ctx, `
		SELECT batch_id, sequence_no, order_no, status, last_error, updated_at
		FROM shein_go_bulk_fulfillment_items
		WHERE batch_id = $1 AND status = 'running'
		ORDER BY sequence_no LIMIT 1
	`, batchID))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return BulkFulfillmentItem{}, false, err
		}
		return item, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BulkFulfillmentItem{}, false, err
	}
	item, err = scanBulkItem(tx.QueryRow(ctx, `
		WITH next AS (
			SELECT batch_id, order_no
			FROM shein_go_bulk_fulfillment_items
			WHERE batch_id = $1 AND status = 'pending'
			ORDER BY sequence_no LIMIT 1 FOR UPDATE
		)
		UPDATE shein_go_bulk_fulfillment_items item
		SET status = 'running', last_error = '', updated_at = now()
		FROM next
		WHERE item.batch_id = next.batch_id AND item.order_no = next.order_no
		RETURNING item.batch_id, item.sequence_no, item.order_no,
			item.status, item.last_error, item.updated_at
	`, batchID))
	if errors.Is(err, pgx.ErrNoRows) {
		return BulkFulfillmentItem{}, false, nil
	}
	if err != nil {
		return BulkFulfillmentItem{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkFulfillmentItem{}, false, err
	}
	return item, true, nil
}

func (s *Store) FinishBulkFulfillmentItem(ctx context.Context, shopKey, orderNo, status, lastError string) (BulkFulfillmentBatch, bool, error) {
	if status != "succeeded" && status != "failed" {
		return BulkFulfillmentBatch{}, false, errors.New("invalid SHEIN automatic batch item status")
	}
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var batchID, failedOrderNo, lastBatchError string
	err = tx.QueryRow(ctx, `
		SELECT b.id, b.failed_order_no, b.last_error
		FROM shein_go_bulk_fulfillment_batches b
		JOIN shein_go_bulk_fulfillment_items i ON i.batch_id = b.id
		WHERE b.shop_key = $1 AND b.status = 'running'
			AND i.order_no = $2 AND i.status = 'running'
		ORDER BY b.created_at DESC LIMIT 1 FOR UPDATE OF b
	`, shopKey, orderNo).Scan(&batchID, &failedOrderNo, &lastBatchError)
	if errors.Is(err, pgx.ErrNoRows) {
		return BulkFulfillmentBatch{}, false, nil
	}
	if err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE shein_go_bulk_fulfillment_items
		SET status = $3, last_error = $4, updated_at = now()
		WHERE batch_id = $1 AND order_no = $2 AND status = 'running'
	`, batchID, orderNo, status, lastError); err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	var succeeded, failed, remaining int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE status = 'succeeded'),
			COUNT(*) FILTER (WHERE status = 'failed'),
			COUNT(*) FILTER (WHERE status IN ('pending', 'running'))
		FROM shein_go_bulk_fulfillment_items WHERE batch_id = $1
	`, batchID).Scan(&succeeded, &failed, &remaining); err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	if status == "failed" {
		failedOrderNo = orderNo
		lastBatchError = lastError
	}
	batchStatus := bulkFulfillmentBatchStatus(failed, remaining)
	if _, err := tx.Exec(ctx, `
		UPDATE shein_go_bulk_fulfillment_batches
		SET status = $2, succeeded_orders = $3, failed_orders = $4,
			failed_order_no = $5, last_error = $6,
			updated_at = now(),
			completed_at = CASE WHEN $2 IN ('completed', 'completed_with_errors') THEN now() ELSE NULL END
		WHERE id = $1
	`, batchID, batchStatus, succeeded, failed, failedOrderNo, lastBatchError); err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	batch, err := scanBulkBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.id = $1`, batchID))
	if err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkFulfillmentBatch{}, false, err
	}
	return batch, true, nil
}

func bulkFulfillmentBatchStatus(failed, remaining int) string {
	if remaining > 0 {
		return "running"
	}
	if failed > 0 {
		return "completed_with_errors"
	}
	return "completed"
}

func (s *Store) RestartStoppedBulkItem(ctx context.Context, shopKey, orderNo string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var batchID string
	err = tx.QueryRow(ctx, `
		SELECT b.id
		FROM shein_go_bulk_fulfillment_batches b
		JOIN shein_go_bulk_fulfillment_items i ON i.batch_id = b.id
		WHERE b.shop_key = $1 AND b.status = 'stopped'
			AND b.failed_order_no = $2 AND i.order_no = $2 AND i.status = 'failed'
			AND NOT EXISTS (
				SELECT 1 FROM shein_go_bulk_fulfillment_batches running
				WHERE running.shop_key = b.shop_key AND running.status = 'running'
			)
		ORDER BY b.created_at DESC LIMIT 1 FOR UPDATE OF b
	`, shopKey, orderNo).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE shein_go_bulk_fulfillment_items
		SET status = 'running', last_error = '', updated_at = now()
		WHERE batch_id = $1 AND order_no = $2
	`, batchID, orderNo); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE shein_go_bulk_fulfillment_batches
		SET status = 'running', failed_orders = 0, failed_order_no = '',
			last_error = '', updated_at = now(), completed_at = NULL
		WHERE id = $1
	`, batchID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func scanBulkBatch(scanner autoJobScanner) (BulkFulfillmentBatch, error) {
	var batch BulkFulfillmentBatch
	err := scanner.Scan(&batch.ID, &batch.Status, &batch.TotalOrders,
		&batch.SucceededOrders, &batch.FailedOrders, &batch.CurrentOrderNo,
		&batch.FailedOrderNo, &batch.LastError, &batch.CreatedAt,
		&batch.UpdatedAt, &batch.CompletedAt)
	return batch, err
}

func scanBulkItem(scanner autoJobScanner) (BulkFulfillmentItem, error) {
	var item BulkFulfillmentItem
	err := scanner.Scan(&item.BatchID, &item.Sequence, &item.OrderNo,
		&item.Status, &item.LastError, &item.UpdatedAt)
	return item, err
}
