package shein

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type ShippingQuote struct {
	PreRequestID         string
	OrderNo              string
	WarehouseAddressCode string
	Candidates           []ShippingQuoteCandidate
}

type ShippingQuoteCandidate struct {
	ExpressChannelCode string
	ExpressID          *int
	ExpressIDCode      string
	ExpressShortName   string
	PerformanceCost    string
	CurrencyCode       string
	EstimateMinDay     *int
	EstimateMaxDay     *int
}

func (s *Store) SaveShippingQuote(ctx context.Context, shopKey string, quote ShippingQuote) error {
	shopKey = strings.TrimSpace(shopKey)
	quote.PreRequestID = strings.TrimSpace(quote.PreRequestID)
	quote.OrderNo = strings.TrimSpace(quote.OrderNo)
	quote.WarehouseAddressCode = strings.TrimSpace(quote.WarehouseAddressCode)
	if shopKey == "" || quote.PreRequestID == "" || quote.OrderNo == "" || quote.WarehouseAddressCode == "" {
		return errors.New("shop, preRequestId, order and warehouse are required for a shipping quote")
	}
	if len(quote.Candidates) == 0 {
		return errors.New("shipping quote must contain at least one channel candidate")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin SHEIN quote snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO shein_go_shipping_quotes (
			shop_key, pre_request_id, order_no, warehouse_address_code
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING
	`, shopKey, quote.PreRequestID, quote.OrderNo, quote.WarehouseAddressCode); err != nil {
		return fmt.Errorf("store SHEIN shipping quote: %w", err)
	}
	for _, candidate := range quote.Candidates {
		candidate.ExpressChannelCode = strings.TrimSpace(candidate.ExpressChannelCode)
		if candidate.ExpressChannelCode == "" {
			return errors.New("shipping quote candidate is missing expressChannelCode")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO shein_go_shipping_quote_candidates (
				shop_key, pre_request_id, express_channel_code, express_id,
				express_id_code, express_short_name, performance_cost,
				currency_code, estimate_min_day, estimate_max_day
			) VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10)
			ON CONFLICT DO NOTHING
		`, shopKey, quote.PreRequestID, candidate.ExpressChannelCode, candidate.ExpressID,
			candidate.ExpressIDCode, candidate.ExpressShortName, nullableDecimal(candidate.PerformanceCost),
			candidate.CurrencyCode, candidate.EstimateMinDay, candidate.EstimateMaxDay); err != nil {
			return fmt.Errorf("store SHEIN shipping quote candidate: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit SHEIN quote snapshot: %w", err)
	}
	return nil
}

func (s *Store) ReserveLabelPurchaseChoice(
	ctx context.Context,
	shopKey, preRequestID, selectedChannelCode, idempotencyKey string,
) (bool, error) {
	return s.ReserveLabelPurchaseSelection(
		ctx, shopKey, preRequestID, selectedChannelCode, idempotencyKey,
		"manual", "operator_selected",
	)
}

func (s *Store) ReserveLabelPurchaseSelection(
	ctx context.Context,
	shopKey, preRequestID, selectedChannelCode, idempotencyKey, selectionSource, selectionReason string,
) (bool, error) {
	shopKey = strings.TrimSpace(shopKey)
	preRequestID = strings.TrimSpace(preRequestID)
	selectedChannelCode = strings.TrimSpace(selectedChannelCode)
	selectionSource = strings.TrimSpace(selectionSource)
	selectionReason = strings.TrimSpace(selectionReason)
	if shopKey == "" || preRequestID == "" || selectedChannelCode == "" || idempotencyKey == "" {
		return false, errors.New("shop, preRequestId, selected channel and idempotency key are required")
	}
	if selectionSource != "manual" && selectionSource != "automatic" {
		return false, errors.New("selection source must be manual or automatic")
	}
	if selectionReason == "" {
		return false, errors.New("selection reason is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin SHEIN purchase snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingIdempotencyKey string
	err = tx.QueryRow(ctx, `
		SELECT operation_idempotency_key
		FROM shein_label_purchase_choices
		WHERE shop_key = $1 AND pre_request_id = $2
	`, shopKey, preRequestID).Scan(&existingIdempotencyKey)
	if err == nil {
		if existingIdempotencyKey != idempotencyKey {
			return false, errors.New("SHEIN quote already entered a different purchase transaction")
		}
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("check existing SHEIN purchase snapshot: %w", err)
	}

	selected := purchaseCandidate{}
	err = tx.QueryRow(ctx, `
		SELECT quote.order_no, quote.warehouse_address_code,
			candidate.express_channel_code, candidate.express_id,
			candidate.express_id_code, candidate.express_short_name,
			COALESCE(candidate.performance_cost::text, ''), candidate.currency_code,
			candidate.estimate_min_day, candidate.estimate_max_day
		FROM shein_go_shipping_quotes quote
		JOIN shein_go_shipping_quote_candidates candidate
			USING (shop_key, pre_request_id)
		WHERE quote.shop_key = $1 AND quote.pre_request_id = $2
			AND candidate.express_channel_code = $3
	`, shopKey, preRequestID, selectedChannelCode).Scan(
		&selected.OrderNo, &selected.WarehouseAddressCode,
		&selected.ExpressChannelCode, &selected.ExpressID,
		&selected.ExpressIDCode, &selected.ExpressShortName,
		&selected.PerformanceCost, &selected.CurrencyCode,
		&selected.EstimateMinDay, &selected.EstimateMaxDay,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var quoteExists bool
		if existsErr := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM shein_go_shipping_quotes
				WHERE shop_key = $1 AND pre_request_id = $2
			)
		`, shopKey, preRequestID).Scan(&quoteExists); existsErr != nil {
			return false, fmt.Errorf("check SHEIN shipping quote: %w", existsErr)
		}
		if !quoteExists {
			return false, nil
		}
		return false, errors.New("selected channel is not part of the stored SHEIN quote")
	}
	if err != nil {
		return false, fmt.Errorf("load selected SHEIN quote candidate: %w", err)
	}
	if selected.PerformanceCost == "" {
		return false, errors.New("selected SHEIN channel has no comparable performance cost")
	}

	candidateQuery := `
		SELECT quote.warehouse_address_code,
			candidate.express_channel_code, candidate.express_id,
			candidate.express_id_code, candidate.express_short_name,
			candidate.performance_cost::text, candidate.currency_code,
			candidate.estimate_min_day, candidate.estimate_max_day
		FROM shein_go_shipping_quotes quote
		JOIN shein_go_shipping_quote_candidates candidate USING (shop_key, pre_request_id)
		WHERE quote.shop_key = $1 AND quote.pre_request_id = $2
			AND candidate.performance_cost IS NOT NULL AND candidate.currency_code = $3
		ORDER BY candidate.performance_cost, quote.warehouse_address_code,
			candidate.express_channel_code
		LIMIT 3
	`
	candidateArgs := []any{shopKey, preRequestID, selected.CurrencyCode}
	if selectionSource == "automatic" {
		candidateQuery = `
			SELECT quote.warehouse_address_code,
				candidate.express_channel_code, candidate.express_id,
				candidate.express_id_code, candidate.express_short_name,
				candidate.performance_cost::text, candidate.currency_code,
				candidate.estimate_min_day, candidate.estimate_max_day
			FROM shein_go_shipping_quotes quote
			JOIN shein_go_shipping_quote_candidates candidate USING (shop_key, pre_request_id)
			WHERE quote.shop_key = $1 AND quote.order_no = $2
				AND candidate.performance_cost IS NOT NULL AND candidate.currency_code = $3
				AND quote.quoted_at BETWEEN
					(SELECT quoted_at - interval '2 minutes' FROM shein_go_shipping_quotes
					 WHERE shop_key = $1 AND pre_request_id = $4)
					AND
					(SELECT quoted_at + interval '2 minutes' FROM shein_go_shipping_quotes
					 WHERE shop_key = $1 AND pre_request_id = $4)
			ORDER BY candidate.performance_cost, quote.warehouse_address_code,
				candidate.express_channel_code
			LIMIT 3
		`
		candidateArgs = []any{shopKey, selected.OrderNo, selected.CurrencyCode, preRequestID}
	}
	rows, err := tx.Query(ctx, candidateQuery, candidateArgs...)
	if err != nil {
		return false, fmt.Errorf("load SHEIN low-price candidates: %w", err)
	}
	candidates := make([]purchaseCandidate, 0, 3)
	for rows.Next() {
		candidate := purchaseCandidate{}
		if err := rows.Scan(
			&candidate.WarehouseAddressCode, &candidate.ExpressChannelCode, &candidate.ExpressID,
			&candidate.ExpressIDCode, &candidate.ExpressShortName,
			&candidate.PerformanceCost, &candidate.CurrencyCode,
			&candidate.EstimateMinDay, &candidate.EstimateMaxDay,
		); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan SHEIN low-price candidate: %w", err)
		}
		candidate.PriceRank = len(candidates) + 1
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("read SHEIN low-price candidates: %w", err)
	}
	rows.Close()
	if len(candidates) == 0 {
		return false, errors.New("stored SHEIN quote has no comparable price candidates")
	}

	var selectedRank *int
	for _, candidate := range candidates {
		if candidate.WarehouseAddressCode == selected.WarehouseAddressCode &&
			candidate.ExpressChannelCode == selected.ExpressChannelCode {
			rank := candidate.PriceRank
			selectedRank = &rank
			break
		}
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO shein_label_purchase_choices (
			shop_key, pre_request_id, operation_idempotency_key, order_no,
			selection_source, selected_price_rank, selected_warehouse_address_code,
			selected_express_id, selected_express_id_code, selected_express_channel_code,
			selected_express_short_name, selected_performance_cost, selected_currency_code,
			selected_estimate_min_day, selected_estimate_max_day, selection_reason
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
			$12::numeric, $13, $14, $15, $16
		)
		ON CONFLICT DO NOTHING
	`, shopKey, preRequestID, idempotencyKey, selected.OrderNo, selectionSource, selectedRank,
		selected.WarehouseAddressCode, selected.ExpressID, selected.ExpressIDCode,
		selected.ExpressChannelCode, selected.ExpressShortName, selected.PerformanceCost,
		selected.CurrencyCode, selected.EstimateMinDay, selected.EstimateMaxDay, selectionReason)
	if err != nil {
		return false, fmt.Errorf("store SHEIN selected purchase choice: %w", err)
	}
	if tag.RowsAffected() == 1 {
		for _, candidate := range candidates {
			if _, err := tx.Exec(ctx, `
				INSERT INTO shein_label_purchase_candidates (
					shop_key, pre_request_id, price_rank, warehouse_address_code,
					express_id, express_id_code, express_channel_code, express_short_name,
					performance_cost, currency_code, estimate_min_day, estimate_max_day, is_selected
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11, $12, $13)
			`, shopKey, preRequestID, candidate.PriceRank, candidate.WarehouseAddressCode,
				candidate.ExpressID, candidate.ExpressIDCode, candidate.ExpressChannelCode,
				candidate.ExpressShortName, candidate.PerformanceCost, candidate.CurrencyCode,
				candidate.EstimateMinDay, candidate.EstimateMaxDay,
				candidate.WarehouseAddressCode == selected.WarehouseAddressCode &&
					candidate.ExpressChannelCode == selected.ExpressChannelCode); err != nil {
				return false, fmt.Errorf("store SHEIN low-price purchase candidate: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit SHEIN purchase snapshot: %w", err)
	}
	return true, nil
}

func (s *Store) UpdateLabelPurchaseResult(ctx context.Context, shopKey, preRequestID, placeRequestID, deliveryNo string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE shein_label_purchase_choices
		SET place_request_id = COALESCE(NULLIF($3, ''), place_request_id),
			delivery_no = COALESCE(NULLIF($4, ''), delivery_no)
		WHERE shop_key = $1 AND pre_request_id = $2
	`, shopKey, preRequestID, placeRequestID, deliveryNo)
	if err != nil {
		return fmt.Errorf("link SHEIN purchase snapshot result: %w", err)
	}
	return nil
}

type purchaseCandidate struct {
	OrderNo              string
	WarehouseAddressCode string
	ExpressChannelCode   string
	ExpressID            *int64
	ExpressIDCode        string
	ExpressShortName     string
	PerformanceCost      string
	CurrencyCode         string
	EstimateMinDay       *int
	EstimateMaxDay       *int
	PriceRank            int
}

func nullableDecimal(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
