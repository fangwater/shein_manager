"use strict";

const sheinXLWMS = {
  accounts: [],
  accountFailures: 0,
  accountRetryTimer: null,
  lastOrderNo: "",
  warehouseController: null,
  warehouseOrderNo: ""
};

function setXLWMSPill(tone, text) {
  const pill = byId("xlwms-pill");
  const dot = byId("session-dot");
  pill.className = "token-pill" + (tone === "ready" ? "" : " " + tone);
  dot.className = "dot" + (tone === "ready" ? "" : tone === "error" ? " error" : " warning");
  byId("session-user").textContent = text;
}

async function loadXLWMSAccounts(button) {
  if (sheinXLWMS.accountRetryTimer) {
    window.clearTimeout(sheinXLWMS.accountRetryTimer);
    sheinXLWMS.accountRetryTimer = null;
  }
  await busy(button || null, async function () {
    setXLWMSPill("warning", "检查中");
    try {
      const payload = await request("oms-platform-orders/accounts");
      sheinXLWMS.accountFailures = 0;
      sheinXLWMS.accounts = Array.isArray(payload.data) ? payload.data : [];
      byId("oms-account").innerHTML = '<option value="all">全部账户</option>' + sheinXLWMS.accounts.map(function (account) {
        const key = display(account.key);
        const label = display(account.label, key.toUpperCase());
        return '<option value="' + escapeHTML(key) + '">' + escapeHTML(label) + "</option>";
      }).join("");
      if (!sheinXLWMS.accounts.length) {
        setXLWMSPill("warning", "暂无可用账户");
        return;
      }
      setXLWMSPill("ready", "实时查询可用 · " + sheinXLWMS.accounts.length + " 个账户");
    } catch (error) {
      sheinXLWMS.accounts = [];
      sheinXLWMS.accountFailures += 1;
      setXLWMSPill("error", "查询服务异常");
      if (button) toast(error.message, true);
      const retryDelay = Math.min(30000, 2000 * Math.pow(2, sheinXLWMS.accountFailures - 1));
      sheinXLWMS.accountRetryTimer = window.setTimeout(function () {
        loadXLWMSAccounts();
      }, retryDelay);
    }
  });
}

function xlwmsStatusMeta(status, statusText) {
  const values = {
    0: ["待处理", "pending"],
    1: ["待获取平台面单", "pending"],
    2: ["处理中", "neutral"],
    3: ["已发货", ""],
    4: ["已取消", "failed"],
    5: ["异常", "failed"],
    6: ["待开票", "pending"]
  };
  const meta = values[Number(status)] || [display(statusText, "未知状态"), "neutral"];
  return { label: statusText || meta[0], tone: meta[1] };
}

function xlwmsLookupRows(result) {
  const rows = [];
  (result.accounts || []).forEach(function (lookup) {
    (lookup.orders || []).forEach(function (order) {
      rows.push({ account: lookup.account, order: order });
    });
  });
  return rows;
}

function renderXLWMSLookup(result) {
  const rows = xlwmsLookupRows(result);
  const body = byId("oms-status-rows");
  const table = body.closest(".table-shell");
  table.classList.toggle("is-empty", rows.length === 0);
  body.innerHTML = rows.map(function (item) {
    const order = item.order || {};
    const status = xlwmsStatusMeta(order.status, order.status_text);
    const platform = display(order.platform_code, "未返回");
    const platformTone = /shein/i.test(platform) ? "neutral" : "failed";
    return '<tr><td><div class="order-id"><strong>' + escapeHTML(display(order.platform_order_no, result.platform_order_no)) +
      '</strong><small>' + escapeHTML(display(order.order_time, "未返回下单时间")) +
      '</small></div></td><td><div class="order-id"><strong>' + escapeHTML(display(order.oms_order_no)) +
      '</strong><small>' + escapeHTML(display(order.create_time, "未返回创建时间")) +
      '</small></div></td><td><span class="status-badge neutral">' + escapeHTML(display(item.account).toUpperCase()) +
      '</span></td><td><span class="status-badge ' + platformTone + '">' + escapeHTML(platform) +
      '</span></td><td><span class="status-badge ' + status.tone + '">状态 ' + escapeHTML(display(order.status)) + " · " +
      escapeHTML(status.label) + '</span></td><td>' + escapeHTML(display(order.send_warehouse_code, "等待分仓")) +
      '</td><td>' + escapeHTML(display(order.tracking_number)) +
      '</td><td>' + escapeHTML(display(order.audit_time)) + "</td></tr>";
  }).join("");
  byId("metric-oms-order").textContent = display(result.platform_order_no, "-");
  byId("metric-oms-matches").textContent = String(result.match_count || rows.length);
  byId("metric-oms-accounts").textContent = String((result.accounts || []).length);
  byId("metric-oms-time").textContent = result.queried_at ? formatTaskTime(result.queried_at) : "-";
  byId("nav-oms-status-count").textContent = String(result.match_count || rows.length);
  const empty = byId("oms-status-empty");
  empty.querySelector("strong").textContent = rows.length ? "" : "领星未找到同号订单";
  empty.querySelector("span").textContent = rows.length ? "" : "已查询所有选择的领星账户";
  if ((result.failures || []).length) {
    toast("部分领星账户查询失败，请稍后重试", true);
  }
}

async function queryXLWMSOrder(button) {
  const orderNo = byId("oms-order-number").value.trim();
  if (!orderNo) {
    toast("请输入 SHEIN 平台订单号", true);
    return;
  }
  const account = byId("oms-account").value || "all";
  sheinXLWMS.lastOrderNo = orderNo;
  await busy(button || null, async function () {
    try {
      const payload = await request("oms-platform-orders/" + encodeURIComponent(orderNo) + "?account=" + encodeURIComponent(account));
      renderXLWMSLookup(payload.data || {});
    } catch (error) {
      toast(error.message, true);
    }
  });
}

function openXLWMSOrderLookup(orderNo) {
  selectView("oms-statuses");
  byId("crumb-current").textContent = "领星订单状态";
  byId("oms-order-number").value = orderNo || "";
  if (orderNo) queryXLWMSOrder(byId("query-oms-order"));
}

function setXLWMSWarehouseCheck(tone, title, detail) {
  byId("warehouse-check-title").textContent = title;
  const message = byId("warehouse-check-message");
  message.className = "warehouse-check-message " + tone;
  const dotClass = tone === "ready" ? "" : tone === "failed" ? " error" : " warning";
  message.innerHTML = '<span class="dot' + dotClass + '"></span><div><strong>' + escapeHTML(title) +
    '</strong><small>' + escapeHTML(detail || "") + "</small></div>";
}

function xlwmsInventoryWarehouse(record, key) {
  const regions = Array.isArray(record && record.regions) ? record.regions : [];
  for (let regionIndex = 0; regionIndex < regions.length; regionIndex += 1) {
    const warehouses = Array.isArray(regions[regionIndex].warehouses) ? regions[regionIndex].warehouses : [];
    const found = warehouses.find(function (warehouse) { return warehouse.warehouse_key === key; });
    if (found) return found;
  }
  return null;
}

function xlwmsStockCell(warehouse) {
  if (!warehouse) return '<span class="stock-cell blocked"><strong>-</strong><small>未返回</small></span>';
  const ready = warehouse.query_status === "success" && warehouse.sku_found;
  return '<span class="stock-cell ' + (ready ? "ready" : "blocked") + '"><strong>' +
    escapeHTML(quantityText(warehouse.available_amount)) + '</strong><small>' +
    escapeHTML(display(warehouse.reason, ready ? "实时可用" : "不可用")) + "</small></span>";
}

function xlwmsDimensionCM(value, unit) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) return 0;
  return String(unit || "cm").toLowerCase() === "in" ? number * 2.54 : number;
}

function xlwmsWeightGrams(value, unit) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) return 0;
  switch (String(unit || "kg").toLowerCase()) {
  case "g": return number;
  case "lb": return number * 453.59237;
  case "oz": return number * 28.349523125;
  default: return number * 1000;
  }
}

function renderXLWMSPackageResolution(resolution) {
  const panel = byId("package-spec-panel");
  const items = Array.isArray(resolution && resolution.items) ? resolution.items : [];
  panel.hidden = !resolution || (!items.length && !resolution.error);
  if (panel.hidden) return;
  const complete = Boolean(resolution.complete);
  byId("package-spec-title").textContent = complete ? "仓库 SKU 包裹规格已匹配" : "仓库 SKU 包裹规格不完整";
  const badge = byId("package-spec-badge");
  badge.className = "status-badge " + (complete ? "" : "failed");
  badge.textContent = complete ? "校验通过" : "需要处理";
  byId("package-spec-items").innerHTML = items.map(function (item) {
    const ready = Boolean(item.complete);
    const dimensions = ready ? [item.length_cm, item.width_cm, item.height_cm].join(" × ") + " cm · " +
      quantityText(Number(item.weight_kg) * 1000) + " g" : display((item.missing_fields || []).join("、"), "规格未匹配");
    const matched = item.matched_warehouse_sku && item.matched_warehouse_sku !== item.warehouse_sku
      ? " · 匹配 " + item.matched_warehouse_sku : "";
    return '<div class="package-spec-item ' + (ready ? "" : "blocked") + '"><span class="dot' +
      (ready ? "" : " error") + '"></span><div class="package-spec-copy"><strong>' +
      escapeHTML(display(item.warehouse_sku) + matched) + '</strong><small>需发 ' +
      escapeHTML(display(item.quantity, 1)) + " · " + escapeHTML(dimensions) + "</small></div></div>";
  }).join("") || '<div class="package-spec-item blocked"><span class="dot error"></span><div><strong>包裹规格未返回</strong><small>' +
    escapeHTML(display(resolution.error, "请在仓库中台补齐规格")) + "</small></div></div>";

  const packageSpec = resolution.package || {};
  const length = xlwmsDimensionCM(packageSpec.length, packageSpec.dimension_unit);
  const width = xlwmsDimensionCM(packageSpec.width, packageSpec.dimension_unit);
  const height = xlwmsDimensionCM(packageSpec.height, packageSpec.dimension_unit);
  const weight = xlwmsWeightGrams(packageSpec.weight, packageSpec.weight_unit);
  if (complete && length && width && height && weight) {
    byId("package-length").value = quantityText(length);
    byId("package-width").value = quantityText(width);
    byId("package-height").value = quantityText(height);
    byId("package-weight").value = quantityText(weight);
  }
}

function renderXLWMSWarehousePreview(preview) {
  const decision = preview.decision || {};
  const records = Array.isArray(decision.records) ? decision.records : [];
  const defaults = decision.default_thresholds || {
    east_threshold: Number(decision.safety_stock_threshold || 0),
    west_threshold: Number(decision.safety_stock_threshold || 0),
    total_threshold: 0
  };
  byId("inventory-rule").textContent = "规则 " + display(decision.rule_version) + " · 默认安全线 东 " +
    display(defaults.east_threshold, 0) + " / 西 " + display(defaults.west_threshold, 0) + " / 总 " +
    display(defaults.total_threshold, 0);
  byId("inventory-time").textContent = decision.queried_at ? "库存时间 " + formatTaskTime(decision.queried_at) : "库存时间 -";
  byId("inventory-preview-rows").innerHTML = records.map(function (record) {
    const thresholds = record.thresholds || defaults;
    const manualReasons = (record.regions || []).filter(function (region) { return region.requires_manual; }).map(function (region) {
      return display(region.region_name) + "：" + display(region.reason);
    });
    const conclusion = record.requires_manual
      ? '<span class="inventory-conclusion blocked"><strong>转人工</strong><small>' + escapeHTML(manualReasons.join("；") || record.reason) + "</small></span>"
      : '<span class="inventory-conclusion ready"><strong>库存校验通过</strong><small>东西区域库存高于安全线</small></span>';
    return '<tr><td><div class="order-id"><strong>' + escapeHTML(record.sku) + '</strong><small>需发 ' +
      escapeHTML(display(preview.quantities && preview.quantities[record.sku], 0)) + " · 安全线 东" +
      escapeHTML(display(thresholds.east_threshold, 0)) + " / 西" + escapeHTML(display(thresholds.west_threshold, 0)) +
      " / 总" + escapeHTML(display(thresholds.total_threshold, 0)) + "</small></div></td><td>" +
      xlwmsStockCell(xlwmsInventoryWarehouse(record, "DPS002")) + "</td><td>" +
      xlwmsStockCell(xlwmsInventoryWarehouse(record, "ARP_EAST")) + "</td><td>" +
      xlwmsStockCell(xlwmsInventoryWarehouse(record, "DPS004")) + "</td><td>" +
      xlwmsStockCell(xlwmsInventoryWarehouse(record, "ARP_WEST")) + "</td><td>" + conclusion + "</td></tr>";
  }).join("");
  byId("inventory-matrix").hidden = records.length === 0;
  renderXLWMSPackageResolution(decision.package_resolution || null);

  const categories = preview.manual_categories || [];
  if (preview.inventory_error) {
    setXLWMSWarehouseCheck("failed", "领星库存查询不完整", preview.inventory_error);
  } else if (categories.includes("sku_unbound")) {
    setXLWMSWarehouseCheck("failed", "发现未绑定仓库 SKU", (preview.manual_reasons || []).join("；"));
  } else if (categories.includes("warehouse_sku_spec_incomplete")) {
    setXLWMSWarehouseCheck("failed", "仓库 SKU 包裹规格缺失", (preview.manual_reasons || []).join("；"));
  } else if (preview.requires_manual) {
    setXLWMSWarehouseCheck("failed", "库存规则要求转人工", (preview.manual_reasons || []).join("；"));
  } else if (preview.ready) {
    setXLWMSWarehouseCheck("ready", "领星实时库存校验通过", "仓库 SKU 与包裹规格均已匹配");
  } else {
    setXLWMSWarehouseCheck("warning", "领星库存已返回", "请核对库存与包裹规格后继续");
  }
}

async function loadXLWMSWarehousePreview(orderNo) {
  orderNo = orderNo || state.orderNo;
  if (!orderNo) return;
  if (sheinXLWMS.warehouseController) sheinXLWMS.warehouseController.abort();
  const controller = new AbortController();
  sheinXLWMS.warehouseController = controller;
  sheinXLWMS.warehouseOrderNo = orderNo;
  byId("inventory-matrix").hidden = true;
  byId("package-spec-panel").hidden = true;
  setXLWMSWarehouseCheck("pending", "正在连接领星 Go 服务", "实时读取正品产品可用库存");
  const button = byId("refresh-warehouse-preview");
  button.disabled = true;
  button.classList.add("loading");
  try {
    const payload = await request("orders/" + encodeURIComponent(orderNo) + "/warehouse-preview", {
      method: "POST",
      signal: controller.signal
    });
    if (state.orderNo !== orderNo || sheinXLWMS.warehouseOrderNo !== orderNo) return;
    renderXLWMSWarehousePreview(payload.data || {});
  } catch (error) {
    if (error.name === "AbortError" || state.orderNo !== orderNo) return;
    setXLWMSWarehouseCheck("failed", "无法获取领星实时库存", error.message);
  } finally {
    if (sheinXLWMS.warehouseController === controller) {
      sheinXLWMS.warehouseController = null;
      button.disabled = false;
      button.classList.remove("loading");
    }
  }
}

byId("oms-order-form").addEventListener("submit", function (event) {
  event.preventDefault();
  queryXLWMSOrder(event.submitter);
});

byId("refresh-oms-statuses").addEventListener("click", function (event) {
  loadXLWMSAccounts(event.currentTarget);
});

byId("refresh-warehouse-preview").addEventListener("click", function () {
  loadXLWMSWarehousePreview(state.orderNo);
});

document.addEventListener("click", function (event) {
  const button = event.target.closest("[data-oms-order]");
  if (button) openXLWMSOrderLookup(button.dataset.omsOrder);
});

const omsNavigation = document.querySelector('[data-view="oms-statuses"]');
if (omsNavigation) {
  omsNavigation.addEventListener("click", function () {
    byId("crumb-current").textContent = "领星订单状态";
    window.setTimeout(function () { byId("oms-order-number").focus(); }, 0);
  });
}

Promise.resolve(window.sheinShopReady).then(function () {
  loadXLWMSAccounts();
});
