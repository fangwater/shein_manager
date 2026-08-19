package sheinconsole

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/xlwms"
)

const warehouseWatchInterval = 2 * time.Minute

func (s *Server) startWarehouseWatch() {
	if s.xlwms == nil || s.store == nil {
		return
	}
	go func() {
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout+20*time.Second)
				if _, err := s.syncWarehouseWatch(ctx); err != nil && s.logger != nil {
					s.logger.Warn("SHEIN warehouse watch failed", "shop", s.shopKey, "error", sanitizedError(err))
				}
				cancel()
				timer.Reset(warehouseWatchInterval)
			}
		}
	}()
}

func (s *Server) syncWarehouseWatch(ctx context.Context) (int, error) {
	if s.store == nil || s.xlwms == nil {
		return 0, nil
	}
	tasks, err := s.store.ListWarehouseWatchTasks(ctx, s.shopKey, 80)
	if err != nil {
		return 0, err
	}
	checked := 0
	problems := make([]error, 0)
	for _, task := range tasks {
		if ctx.Err() != nil {
			problems = append(problems, ctx.Err())
			break
		}
		if _, err := s.refreshWarehouseWatch(ctx, task); err != nil {
			problems = append(problems, err)
			continue
		}
		checked++
	}
	if err := s.syncFulfillmentAudits(ctx); err != nil {
		problems = append(problems, err)
	}
	return checked, errors.Join(problems...)
}

func (s *Server) recordParcelWatch(ctx context.Context, shopKey, orderNo, warehouse string, parcel lingxingParcelStatus) error {
	if s.store == nil {
		return nil
	}
	task, err := s.store.LatestFulfillmentTask(ctx, shopKey, orderNo)
	if err != nil {
		return err
	}
	update := shein.ParcelWatchUpdate{
		OutboundOrderNo:    firstNonEmpty(parcel.OutboundOrderNo, task.OutboundOrderNo),
		OutboundStatus:     parcel.Status,
		OutboundStatusName: firstNonEmpty(parcel.StatusName, task.OutboundStatusName),
		LabelAttached:      parcel.LabelAttached || task.LabelAttached,
		OMSAccount:         firstNonEmpty(task.OMSAccount, shein.OMSAccountForWarehouse(task.WarehouseAddressCode, warehouse)),
		OMSOrderNo:         task.OMSOrderNo,
		OMSStatusCode:      task.OMSStatusCode,
		OMSStatusKey:       task.OMSStatusKey,
		OMSStatusText:      task.OMSStatusText,
		OMSWarehouseCode:   firstNonEmpty(task.OMSWarehouseCode, warehouse),
		OMSSyncStatus:      firstNonEmpty(task.OMSSyncStatus, "waiting_sync"),
		OMSSyncMessage:     firstNonEmpty(task.OMSSyncMessage, "领星出库单已创建，等待仓库状态"),
	}
	if err := s.store.SaveParcelWatch(ctx, shopKey, orderNo, update); err != nil {
		return err
	}
	if err := s.store.EnsureWarehouseLedgerJob(ctx, shopKey, orderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo); err != nil {
		return err
	}
	s.syncFulfillmentAuditsInBackground()
	return nil
}

func (s *Server) refreshWarehouseWatch(ctx context.Context, task shein.FulfillmentTask) (shein.FulfillmentTask, error) {
	purchase, purchaseWarehouse := s.purchasedWarehouseForTask(ctx, task)
	dpsWarehouse := ""
	if purchaseWarehouse.Account == "dps" {
		dpsWarehouse = purchaseWarehouse.OMSCode
	} else {
		dpsWarehouse = shein.ResolvedDPSWarehouseCode(task.WarehouseAddressCode, "")
		if purchaseWarehouse.OK() {
			dpsWarehouse = ""
		}
	}
	warehouse := firstNonEmpty(purchaseWarehouse.OMSCode, dpsWarehouse, strings.TrimSpace(task.OMSWarehouseCode))
	var parcel lingxingParcelStatus
	var parcelErr error
	if dpsWarehouse != "" {
		parcel, parcelErr = s.lookupLingxingParcel(ctx, dpsWarehouse, task.OrderNo)
		if parcelErr == nil && strings.TrimSpace(parcel.OutboundOrderNo) != "" {
			task.OutboundOrderNo = parcel.OutboundOrderNo
			task.OutboundStatus = parcel.Status
			task.OutboundStatusName = parcel.StatusName
			task.LabelAttached = parcel.LabelAttached
			task.ParcelComplete = lingxingParcelCompletesManualCreate(parcel)
			warehouse = dpsWarehouse
		}
	}
	update := shein.ParcelWatchUpdate{
		OutboundOrderNo:    task.OutboundOrderNo,
		OutboundStatus:     task.OutboundStatus,
		OutboundStatusName: task.OutboundStatusName,
		LabelAttached:      task.LabelAttached,
		OMSAccount:         firstNonEmpty(purchaseWarehouse.Account, task.OMSAccount, shein.OMSAccountForWarehouse(task.WarehouseAddressCode, warehouse)),
		OMSOrderNo:         task.OMSOrderNo,
		OMSStatusCode:      task.OMSStatusCode,
		OMSStatusKey:       task.OMSStatusKey,
		OMSStatusText:      task.OMSStatusText,
		OMSWarehouseCode:   firstNonEmpty(task.OMSWarehouseCode, warehouse),
		OMSSyncStatus:      "waiting_sync",
		OMSSyncMessage:     "正在查询领星仓库状态",
	}
	if parcelErr != nil && strings.TrimSpace(task.OutboundOrderNo) == "" && !shein.RequiresManualParcelCreate(s.shopKey, task.WarehouseAddressCode, warehouse) {
		if decision, ok := sheinPlatformAlreadyFulfilledDecision(s.sheinPlatformFulfillmentStatus(ctx, s.shopKey, task.OrderNo)); ok {
			applySHEINOMSDecision(&update, decision)
			if err := s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update); err != nil {
				return task, err
			}
			_ = s.store.EnsureWarehouseLedgerJob(ctx, s.shopKey, task.OrderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo)
			return s.store.LatestFulfillmentTask(ctx, s.shopKey, task.OrderNo)
		}
		update.OMSSyncStatus = "failed"
		update.OMSSyncMessage = parcelErr.Error()
		_ = s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update)
		return task, parcelErr
	}
	if applyLingxingParcelWarehouseDecision(&update, parcel) {
		if err := s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update); err != nil {
			return task, err
		}
		_ = s.store.EnsureWarehouseLedgerJob(ctx, s.shopKey, task.OrderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo)
		return s.store.LatestFulfillmentTask(ctx, s.shopKey, task.OrderNo)
	}
	platformStatus := s.sheinPlatformFulfillmentStatus(ctx, s.shopKey, task.OrderNo)
	if decision, ok := sheinPlatformAlreadyFulfilledDecision(platformStatus); ok {
		applySHEINOMSDecision(&update, decision)
		if err := s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update); err != nil {
			return task, err
		}
		_ = s.store.EnsureWarehouseLedgerJob(ctx, s.shopKey, task.OrderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo)
		return s.store.LatestFulfillmentTask(ctx, s.shopKey, task.OrderNo)
	}
	if s.xlwms == nil {
		update.OMSSyncStatus = "failed"
		update.OMSSyncMessage = "领星查询服务未配置"
		_ = s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update)
		return task, errors.New(update.OMSSyncMessage)
	}
	preferredAccount := firstNonEmpty(purchaseWarehouse.Account, update.OMSAccount, shein.OMSAccountForWarehouse(task.WarehouseAddressCode, warehouse), "dps")
	dpsLookup, err := s.xlwms.QueryPlatformOrder(ctx, "dps", task.OrderNo)
	if err != nil {
		update.OMSSyncStatus = "failed"
		update.OMSSyncMessage = err.Error()
		_ = s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update)
		return task, err
	}
	arpLookup, err := s.xlwms.QueryPlatformOrder(ctx, "arp", task.OrderNo)
	if err != nil {
		update.OMSSyncStatus = "failed"
		update.OMSSyncMessage = err.Error()
		_ = s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update)
		return task, err
	}
	if purchaseWarehouse.Account != "arp" &&
		shein.RequiresManualParcelCreate(s.shopKey, task.WarehouseAddressCode, warehouse) &&
		strings.TrimSpace(firstNonEmpty(parcel.OutboundOrderNo, task.OutboundOrderNo)) == "" &&
		len(activeOMSPlatformOrders(arpLookup)) == 0 {
		update.OMSAccount = "dps"
		update.OMSWarehouseCode = firstNonEmpty(purchaseWarehouse.OMSCode, dpsWarehouse, update.OMSWarehouseCode)
		created, createErr := s.ensureAutomaticDPSParcel(ctx, s.shopKey, task)
		if createErr != nil {
			update.OMSSyncStatus = "waiting_sync"
			update.OMSSyncMessage = "DPS 自动建单失败，等待重试：" + createErr.Error()
			if err := s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update); err != nil {
				return task, err
			}
			_ = s.store.EnsureWarehouseLedgerJob(ctx, s.shopKey, task.OrderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo)
			return task, createErr
		}
		parcel = created
		if strings.TrimSpace(parcel.OutboundOrderNo) != "" {
			task.OutboundOrderNo = parcel.OutboundOrderNo
			task.OutboundStatus = parcel.Status
			task.OutboundStatusName = parcel.StatusName
			task.LabelAttached = parcel.LabelAttached
			task.ParcelComplete = lingxingParcelCompletesManualCreate(parcel)
			warehouse = firstNonEmpty(dpsWarehouse, warehouse)
		}
		if applyLingxingParcelWarehouseDecision(&update, parcel) {
			if err := s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update); err != nil {
				return task, err
			}
			_ = s.store.EnsureWarehouseLedgerJob(ctx, s.shopKey, task.OrderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo)
			return s.store.LatestFulfillmentTask(ctx, s.shopKey, task.OrderNo)
		}
		update.OMSSyncStatus = "waiting_sync"
		update.OMSSyncMessage = "等待 DPS 自动建领星出库单"
		if err := s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update); err != nil {
			return task, err
		}
		_ = s.store.EnsureWarehouseLedgerJob(ctx, s.shopKey, task.OrderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo)
		return s.store.LatestFulfillmentTask(ctx, s.shopKey, task.OrderNo)
	}
	expected, opposite, account := chooseOMSLookups(preferredAccount, dpsLookup, arpLookup)
	if purchaseWarehouse.OK() {
		warehouse = purchaseWarehouse.OMSCode
		update.OMSAccount = purchaseWarehouse.Account
		update.OMSWarehouseCode = purchaseWarehouse.OMSCode
	} else if account == "arp" {
		warehouse = firstNonEmpty(firstOMSWarehouse(arpLookup), "ARP")
		update.OMSWarehouseCode = warehouse
	}
	startedAt := task.CreatedAt
	if startedAt.IsZero() {
		startedAt = task.UpdatedAt
	}
	decision := decideSHEINOMSPlatformOrder(expected, opposite, warehouse, startedAt, time.Now(), platformStatus)
	if decision.State == "pending" && purchaseWarehouse.OK() {
		if assignErr := s.assignPendingOMSPlatformOrder(ctx, task, purchase, purchaseWarehouse, expected); assignErr != nil {
			decision.Message = "领星待处理，仓库物流自动分配失败，等待后台重试：" + assignErr.Error()
		} else {
			refreshed, refreshErr := s.xlwms.QueryPlatformOrder(ctx, purchaseWarehouse.Account, task.OrderNo)
			if refreshErr == nil && len(activeOMSPlatformOrders(refreshed)) == 1 {
				expected = refreshed
				decision = decideSHEINOMSPlatformOrder(expected, opposite, purchaseWarehouse.OMSCode, startedAt, time.Now(), platformStatus)
			}
			if decision.State == "pending" {
				decision.Message = "领星仓库物流已自动分配，等待状态推进"
			}
		}
	}
	applySHEINOMSDecision(&update, decision)
	if decision.Target.OMSOrderNo != "" {
		update.OMSAccount = firstNonEmpty(purchaseWarehouse.Account, expected.Account, account)
		update.OMSWarehouseCode = firstNonEmpty(purchaseWarehouse.OMSCode, decision.Target.SendWarehouseCode, update.OMSWarehouseCode)
	}
	if err := s.store.SaveParcelWatch(ctx, s.shopKey, task.OrderNo, update); err != nil {
		return task, err
	}
	_ = s.store.EnsureWarehouseLedgerJob(ctx, s.shopKey, task.OrderNo, task.WarehouseAddressCode, task.ExpressChannelCode, task.PlaceRequestID, task.DeliveryNo)
	return s.store.LatestFulfillmentTask(ctx, s.shopKey, task.OrderNo)
}

type sheinOMSDecision struct {
	State          string
	Message        string
	Verified       bool
	ManualRequired bool
	Target         xlwms.PlatformOrder
}

func applyLingxingParcelWarehouseDecision(update *shein.ParcelWatchUpdate, parcel lingxingParcelStatus) bool {
	if update == nil || strings.TrimSpace(parcel.OutboundOrderNo) == "" || parcel.Status == nil {
		return false
	}
	update.OutboundOrderNo = parcel.OutboundOrderNo
	update.OutboundStatus = parcel.Status
	update.OutboundStatusName = firstNonEmpty(parcel.StatusName, lingxingOutboundStatusName(*parcel.Status))
	update.LabelAttached = parcel.LabelAttached || update.LabelAttached
	update.OMSOrderNo = firstNonEmpty(update.OMSOrderNo, parcel.OutboundOrderNo)
	update.OMSStatusCode = parcel.Status
	update.OMSStatusKey = lingxingOutboundStatusKey(*parcel.Status)
	update.OMSStatusText = firstNonEmpty(parcel.StatusName, lingxingOutboundStatusName(*parcel.Status))
	switch *parcel.Status {
	case 0, 1:
		update.OMSSyncStatus = "waiting_sync"
		update.OMSSyncMessage = "领星出库单已创建，等待仓库处理"
		return true
	case 2:
		update.OMSSyncStatus = "waiting_sync"
		update.OMSSyncMessage = "领星仓库处理中"
		return true
	case 3:
		update.OMSSyncStatus = "verified"
		update.OMSSyncMessage = "领星已出库"
		return true
	case 4, 5, 6, 7:
		update.OMSSyncStatus = "manual_required"
		update.OMSSyncMessage = "领星出库单 " + firstNonEmpty(parcel.StatusName, lingxingOutboundStatusName(*parcel.Status))
		return true
	default:
		return false
	}
}

func lingxingOutboundStatusKey(status int) string {
	switch status {
	case 0:
		return "pending"
	case 1:
		return "awaiting_platform_label"
	case 2:
		return "processing"
	case 3:
		return "shipped"
	case 4:
		return "canceled"
	case 5:
		return "exception"
	case 6:
		return "intercepted"
	case 7:
		return "label_exception"
	default:
		return ""
	}
}

func (s *Server) sheinPlatformFulfillmentStatus(ctx context.Context, shopKey, orderNo string) string {
	if s.store == nil {
		return ""
	}
	_, normalized, err := s.store.OrderFulfillmentState(ctx, shopKey, orderNo)
	if err == nil && strings.TrimSpace(normalized) != "" && normalized != "unknown" {
		return normalized
	}
	detail, detailErr := s.store.OrderDetail(ctx, shopKey, orderNo)
	if detailErr != nil {
		return strings.TrimSpace(normalized)
	}
	return shein.NormalizeOrderStatus(orderStatusFromMap(detail))
}

func (s *Server) purchasedWarehouseForTask(ctx context.Context, task shein.FulfillmentTask) (shein.LabelPurchaseRecord, shein.PurchasedWarehouse) {
	if s.store == nil {
		return shein.LabelPurchaseRecord{}, shein.ResolvePurchasedWarehouse(task.WarehouseAddressCode, nil)
	}
	record, err := s.store.LatestLabelPurchase(ctx, s.shopKey, task.OrderNo)
	if err != nil {
		return shein.LabelPurchaseRecord{}, shein.ResolvePurchasedWarehouse(task.WarehouseAddressCode, nil)
	}
	return record, record.ResolvedWarehouse()
}

func (s *Server) assignPendingOMSPlatformOrder(
	ctx context.Context,
	task shein.FulfillmentTask,
	purchase shein.LabelPurchaseRecord,
	warehouse shein.PurchasedWarehouse,
	lookup xlwms.PlatformOrderLookup,
) error {
	if s.xlwms == nil {
		return errors.New("领星查询服务未配置")
	}
	if !warehouse.OK() {
		return errors.New("买面单记录没有可用的发货仓库")
	}
	if strings.TrimSpace(purchase.DeliveryNo) == "" && strings.TrimSpace(task.DeliveryNo) == "" && strings.TrimSpace(task.WaybillNo) == "" {
		return errors.New("买面单记录尚未确认运单号")
	}
	if len(lookup.Orders) != 1 {
		return errors.New("领星未找到唯一的同号平台订单")
	}
	order := lookup.Orders[0]
	if order.Status != 0 {
		return errors.New("领星平台订单已不在待处理状态")
	}
	if code := strings.TrimSpace(order.SendWarehouseCode); code != "" && !strings.EqualFold(code, warehouse.OMSCode) {
		return errors.New("领星已有不同的发货仓库")
	}
	preview, err := s.xlwms.PreviewWarehouseAssignment(ctx, warehouse.Account, task.OrderNo)
	if err != nil {
		return err
	}
	if !preview.Ready || len(preview.Routes) != 1 || len(preview.Unresolved) != 0 {
		reason := "无法根据已购面单匹配实际仓库"
		if len(preview.Unresolved) > 0 && strings.TrimSpace(preview.Unresolved[0].Reason) != "" {
			reason = strings.TrimSpace(preview.Unresolved[0].Reason)
		}
		return errors.New(reason)
	}
	if !strings.EqualFold(strings.TrimSpace(preview.Routes[0].WarehouseCode), warehouse.OMSCode) {
		return errors.New("分仓预览与买单仓库不一致")
	}
	result, err := s.xlwms.AssignWarehouse(ctx, warehouse.Account, task.OrderNo, xlwms.AutoMatchCarrier)
	if err != nil {
		return err
	}
	if result.Total != 1 || result.Success != 1 || result.Failed != 0 {
		message := "领星未确认仓库分配结果"
		if len(result.Failures) > 0 && strings.TrimSpace(result.Failures[0].Error) != "" {
			message = strings.TrimSpace(result.Failures[0].Error)
		}
		return errors.New(message)
	}
	return nil
}

func sheinPlatformAlreadyFulfilledDecision(platformStatus string) (sheinOMSDecision, bool) {
	if !shein.OrderFulfilledOnPlatform(platformStatus) {
		return sheinOMSDecision{}, false
	}
	normalized := shein.NormalizeOrderStatus(platformStatus)
	message := "SHEIN 已发货，自动归档，无需等待领星出库单"
	text := "SHEIN 已发货"
	if normalized == "delivered" {
		message = "SHEIN 已签收，自动归档，无需等待领星出库单"
		text = "SHEIN 已签收"
	}
	return sheinOMSDecision{
		State:    normalized,
		Message:  message,
		Verified: true,
		Target:   xlwms.PlatformOrder{StatusKey: normalized, StatusText: text},
	}, true
}

func applySHEINOMSDecision(update *shein.ParcelWatchUpdate, decision sheinOMSDecision) {
	if update == nil {
		return
	}
	if decision.Target.OMSOrderNo != "" {
		update.OMSOrderNo = decision.Target.OMSOrderNo
		update.OMSStatusCode = &decision.Target.Status
		update.OMSWarehouseCode = firstNonEmpty(decision.Target.SendWarehouseCode, update.OMSWarehouseCode)
	}
	update.OMSStatusKey = firstNonEmpty(decision.Target.StatusKey, decision.State, update.OMSStatusKey)
	update.OMSStatusText = firstNonEmpty(decision.Target.StatusText, decision.Message, update.OMSStatusText)
	switch {
	case decision.Verified:
		update.OMSSyncStatus = "verified"
		update.OMSSyncMessage = decision.Message
	case decision.ManualRequired:
		update.OMSSyncStatus = "manual_required"
		update.OMSSyncMessage = decision.Message
	default:
		update.OMSSyncStatus = "waiting_sync"
		update.OMSSyncMessage = decision.Message
	}
}

func decideSHEINOMSPlatformOrder(expected, opposite xlwms.PlatformOrderLookup, expectedWarehouse string, startedAt time.Time, now time.Time, platformStatus string) sheinOMSDecision {
	expectedWarehouse = strings.TrimSpace(expectedWarehouse)
	for _, order := range opposite.Orders {
		if order.Status == 2 || order.Status == 3 {
			return sheinOMSDecision{
				State: "manual_required", ManualRequired: true,
				Message: "领星跨账户重复履约风险：非买单账户存在处理中或已发货订单",
			}
		}
	}
	if decision, ok := sheinPlatformAlreadyFulfilledDecision(platformStatus); ok && len(expected.Orders) == 0 {
		if canceled := firstCanceledOMSOrder(opposite); canceled.OMSOrderNo != "" {
			decision.Target = canceled
			decision.Message = "SHEIN 已完成，对侧领星订单已取消，自动归档"
		}
		return decision
	}
	switch len(expected.Orders) {
	case 0:
		if !startedAt.IsZero() && now.Before(startedAt.Add(30*time.Minute)) {
			return sheinOMSDecision{State: "missing", Message: "领星尚未同步平台订单，等待下一次查询"}
		}
		return sheinOMSDecision{State: "manual_required", ManualRequired: true, Message: "领星漏单：买单仓库对应账户无平台订单"}
	case 1:
	default:
		return sheinOMSDecision{State: "manual_required", ManualRequired: true, Message: "领星买单账户返回多条同号平台订单"}
	}
	target := expected.Orders[0]
	actualWarehouse := strings.TrimSpace(target.SendWarehouseCode)
	if actualWarehouse != "" && expectedWarehouse != "" && !strings.EqualFold(actualWarehouse, expectedWarehouse) {
		return sheinOMSDecision{State: "manual_required", ManualRequired: true, Target: target, Message: "领星仓库不一致"}
	}
	decision := sheinOMSDecision{State: target.StatusKey, Target: target}
	switch target.Status {
	case 0:
		decision.State = "pending"
		decision.Message = "领星待处理"
	case 1:
		decision.State = "awaiting_platform_label"
		decision.Message = "领星待获取平台面单"
	case 2:
		decision.State = "processing"
		decision.Message = "领星仓库处理中"
	case 3:
		decision.State = "shipped"
		decision.Message = "领星已发货"
		decision.Verified = true
	case 4:
		if archived, ok := sheinPlatformAlreadyFulfilledDecision(platformStatus); ok {
			archived.Target = target
			archived.Message = "SHEIN 已完成，领星平台订单已取消，自动归档"
			return archived
		}
		return sheinOMSDecision{State: "manual_required", ManualRequired: true, Target: target, Message: "领星平台订单已取消"}
	case 5:
		return sheinOMSDecision{State: "manual_required", ManualRequired: true, Target: target, Message: "领星平台订单异常"}
	case 6:
		return sheinOMSDecision{State: "manual_required", ManualRequired: true, Target: target, Message: "领星平台订单待开票"}
	default:
		return sheinOMSDecision{State: "manual_required", ManualRequired: true, Target: target, Message: "领星返回未知平台订单状态"}
	}
	return decision
}

func activeOMSPlatformOrders(lookup xlwms.PlatformOrderLookup) []xlwms.PlatformOrder {
	orders := make([]xlwms.PlatformOrder, 0, len(lookup.Orders))
	for _, order := range lookup.Orders {
		if order.Status == 4 {
			continue
		}
		orders = append(orders, order)
	}
	return orders
}

func isReliableOMSWarehouseCode(code string) bool {
	return shein.OMSAccountForResolvedWarehouse(code) != ""
}

func firstOMSWarehouse(lookup xlwms.PlatformOrderLookup) string {
	for _, order := range lookup.Orders {
		if warehouse := strings.TrimSpace(order.SendWarehouseCode); warehouse != "" {
			return warehouse
		}
	}
	return ""
}

func chooseOMSLookups(preferred string, dpsLookup, arpLookup xlwms.PlatformOrderLookup) (xlwms.PlatformOrderLookup, xlwms.PlatformOrderLookup, string) {
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	dpsActive := len(activeOMSPlatformOrders(dpsLookup))
	arpActive := len(activeOMSPlatformOrders(arpLookup))
	switch {
	case preferred == "arp" && arpActive > 0:
		return arpLookup, dpsLookup, "arp"
	case preferred == "dps" && dpsActive > 0:
		return dpsLookup, arpLookup, "dps"
	case arpActive > 0 && dpsActive == 0:
		return arpLookup, dpsLookup, "arp"
	case dpsActive > 0 && arpActive == 0:
		return dpsLookup, arpLookup, "dps"
	case preferred == "arp":
		return arpLookup, dpsLookup, "arp"
	default:
		return dpsLookup, arpLookup, "dps"
	}
}

func firstCanceledOMSOrder(lookup xlwms.PlatformOrderLookup) xlwms.PlatformOrder {
	for _, order := range lookup.Orders {
		if order.Status == 4 {
			return order
		}
	}
	return xlwms.PlatformOrder{}
}

func (s *Server) syncFulfillmentAuditsInBackground() {
	if s.xlwms == nil || s.store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout)
		defer cancel()
		if err := s.syncFulfillmentAudits(ctx); err != nil && s.logger != nil {
			s.logger.Warn("SHEIN fulfillment audit sync failed", "shop", s.shopKey, "error", sanitizedError(err))
		}
	}()
}

func (s *Server) syncFulfillmentAudits(ctx context.Context) error {
	if s.xlwms == nil || s.store == nil {
		return nil
	}
	tasks, err := s.store.ListOMSPlatformOrders(ctx, s.shopKey, "all", 500)
	if err != nil {
		return err
	}
	orders := make([]xlwms.FulfillmentAuditSnapshotOrder, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.OrderNo) == "" {
			continue
		}
		_, purchased := s.purchasedWarehouseForTask(ctx, task)
		warehouse := firstNonEmpty(purchased.OMSCode, task.OMSWarehouseCode, shein.ResolvedOMSWarehouseCode(task.WarehouseAddressCode, ""))
		if !isReliableOMSWarehouseCode(warehouse) {
			warehouse = ""
		}
		status := sheinFulfillmentAuditPlatformStatus(task)
		orders = append(orders, xlwms.FulfillmentAuditSnapshotOrder{
			PlatformOrderNo:    task.OrderNo,
			PlatformStatus:     status,
			PlatformStatusCode: sheinFulfillmentAuditPlatformStatusCode(task),
			PlatformShippingAt: task.OMSQueriedAt,
			WarehouseKey:       firstNonEmpty(purchased.OMSCode, purchased.Account, task.OMSAccount, shein.OMSAccountForWarehouse(task.WarehouseAddressCode, warehouse)),
			WarehouseCode:      warehouse,
			TrackingNumber:     firstNonEmpty(task.WaybillNo, task.DeliveryNo),
		})
	}
	return s.xlwms.SyncFulfillmentAudits(ctx, xlwms.FulfillmentAuditSnapshot{
		Platform: "shein", ShopCode: s.shopKey, ShopName: s.shopName, Orders: orders,
	})
}

func sheinFulfillmentAuditPlatformStatus(task shein.FulfillmentTask) string {
	if shein.OrderFulfilledOnPlatform(task.OMSStatusKey) {
		return "pending_pickup"
	}
	switch strings.TrimSpace(task.OMSStatusKey) {
	case "processing", "shipped", "delivered", "outbound", "canceled", "exception", "pending", "awaiting_platform_label":
		return "pending_pickup"
	default:
		return firstNonEmpty(task.OMSStatusKey, "pending_pickup")
	}
}

func sheinFulfillmentAuditPlatformStatusCode(task shein.FulfillmentTask) *int {
	if shein.OrderFulfilledOnPlatform(task.OMSStatusKey) {
		return nil
	}
	if task.OMSStatusCode != nil && *task.OMSStatusCode >= 0 && *task.OMSStatusCode <= 3 {
		return nil
	}
	return task.OMSStatusCode
}

func (s *Server) listOMSPlatformOrders(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	status := strings.TrimSpace(request.URL.Query().Get("status"))
	if status == "" {
		status = "all"
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	items, err := s.store.ListOMSPlatformOrders(ctx, shopKey, status, 200)
	if err != nil {
		if strings.Contains(err.Error(), "unknown SHEIN OMS platform order status") {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		s.internalError(writer, "list SHEIN OMS platform orders", err)
		return
	}
	counts, err := s.store.CountOMSPlatformOrders(ctx, shopKey)
	if err != nil {
		s.internalError(writer, "count SHEIN OMS platform orders", err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: items, Meta: map[string]any{
		"status": status, "status_counts": counts, "total": len(items),
	}})
}

func (s *Server) syncOMSPlatformOrders(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, "")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	_ = shopKey
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout+20*time.Second)
	defer cancel()
	checked, err := s.syncWarehouseWatch(ctx)
	if err != nil && checked == 0 {
		s.writeAPIError(writer, err)
		return
	}
	items, listErr := s.store.ListOMSPlatformOrders(ctx, s.shopKey, "all", 200)
	if listErr != nil {
		s.internalError(writer, "list SHEIN OMS platform orders after sync", listErr)
		return
	}
	counts, countErr := s.store.CountOMSPlatformOrders(ctx, s.shopKey)
	if countErr != nil {
		s.internalError(writer, "count SHEIN OMS platform orders after sync", countErr)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: items, Meta: map[string]any{
		"checked": checked, "status_counts": counts, "total": len(items),
	}})
}
