"use strict";

const byId = function (id) { return document.getElementById(id); };
const API = "./api/";
const SHOP_STORAGE_KEY = "shein_selected_shop";
const MAX_ACTIONABLE_ORDER_PAGES = 20;
const state = {
  shopKey: new URLSearchParams(window.location.search).get("shop") || sessionStorage.getItem(SHOP_STORAGE_KEY) || "",
  shops: [],
  orders: [],
  manualOrders: [],
  processingJobs: [],
  exceptionJobs: [],
  ledgerJobs: [],
  bulkBatch: null,
  orderPage: 1,
  pageSize: 30,
  detail: null,
  orderNo: "",
  warehouse: null,
  channel: null,
  preRequestId: "",
  tasks: []
};

function escapeHTML(value) {
  return String(value == null ? "" : value).replace(/[&<>"']/g, function (character) {
    return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[character];
  });
}

function display(value, fallback) {
  if (value === 0) return "0";
  return value == null || value === "" ? (fallback || "-") : String(value);
}

function toast(message, isError) {
  const node = byId("toast");
  node.textContent = message;
  node.className = "toast" + (isError ? " error" : "");
  node.hidden = false;
  window.clearTimeout(toast.timer);
  toast.timer = window.setTimeout(function () { node.hidden = true; }, 3600);
}

async function busy(button, job) {
  const content = button ? button.innerHTML : "";
  if (button) {
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
  }
  try {
    return await job();
  } finally {
    if (button) {
      button.disabled = false;
      button.removeAttribute("aria-busy");
      button.innerHTML = content;
    }
  }
}

function uuid() {
  if (window.crypto && crypto.randomUUID) return crypto.randomUUID();
  return Date.now().toString(36) + "-" + Math.random().toString(16).slice(2) + "-shein";
}

function requestFingerprint(data) {
  const source = JSON.stringify(data);
  let hash = 2166136261;
  for (let index = 0; index < source.length; index += 1) {
    hash ^= source.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(16);
}

function idempotencyStorageKey(action, reference, data) {
  return "shein_idempotency:" + state.shopKey + ":" + action + ":" + reference + ":" + requestFingerprint(data);
}

async function request(path, options) {
  const settings = options || {};
  const headers = Object.assign({}, settings.headers || {});
  if (settings.body) headers["Content-Type"] = "application/json";
  if (state.shopKey) headers["X-Shein-Shop"] = state.shopKey;
  const response = await fetch(API + path, Object.assign({ credentials: "same-origin" }, settings, { headers: headers }));
  const payload = await response.json().catch(function () {
    return { success: false, error: "服务返回了无法识别的响应" };
  });
  if (!response.ok || !payload.success) {
    throw new Error(payload.error || "请求失败，HTTP " + response.status);
  }
  return payload;
}

function post(path, data) {
  return request(path, { method: "POST", body: JSON.stringify({ data: data }) });
}

async function sensitivePost(path, data, action, reference) {
  const storageKey = idempotencyStorageKey(action, reference, data);
  let idempotencyKey = localStorage.getItem(storageKey);
  if (!idempotencyKey) {
    idempotencyKey = uuid();
    localStorage.setItem(storageKey, idempotencyKey);
  }
  try {
    return await request(path, {
      method: "POST",
      headers: { "X-Confirm-Shein-Action": action, "Idempotency-Key": idempotencyKey },
      body: JSON.stringify({ data: data })
    });
  } catch (error) {
    localStorage.removeItem(storageKey);
    throw error;
  }
}

function infoOf(payload) {
  return payload && payload.data ? payload.data.info : null;
}

function firstValue(root, keys) {
  if (root == null) return "";
  if (Array.isArray(root)) {
    for (let index = 0; index < root.length; index += 1) {
      const found = firstValue(root[index], keys);
      if (found !== "") return found;
    }
    return "";
  }
  if (typeof root !== "object") return "";
  for (let index = 0; index < keys.length; index += 1) {
    const value = root[keys[index]];
    if (value !== undefined && value !== null && value !== "") return value;
  }
  const values = Object.values(root);
  for (let index = 0; index < values.length; index += 1) {
    if (values[index] && typeof values[index] === "object") {
      const found = firstValue(values[index], keys);
      if (found !== "") return found;
    }
  }
  return "";
}

function listFromInfo(info, keys) {
  if (Array.isArray(info)) return info;
  if (!info || typeof info !== "object") return [];
  for (let index = 0; index < keys.length; index += 1) {
    if (Array.isArray(info[keys[index]])) return info[keys[index]];
  }
  const values = Object.values(info);
  for (let index = 0; index < values.length; index += 1) {
    if (values[index] && typeof values[index] === "object" && !Array.isArray(values[index])) {
      const found = listFromInfo(values[index], keys);
      if (found.length) return found;
    }
  }
  return [];
}

function orderList(payload) {
  return listFromInfo(infoOf(payload), ["list", "data", "records", "rows", "orderList", "order_list", "orders", "orderInfoList", "orderListInfo"]);
}

async function loadOrderPages(baseData, status) {
  const orders = [];
  const seen = new Set();
  for (let page = 1; page <= MAX_ACTIONABLE_ORDER_PAGES; page += 1) {
    const data = Object.assign({}, baseData, { orderStatus: status, page: page });
    const payload = await post("order/list", data);
    const rows = orderList(payload);
    rows.forEach(function (order) {
      const number = orderNumber(order);
      const key = number || JSON.stringify(order);
      if (!seen.has(key)) {
        seen.add(key);
        orders.push(order);
      }
    });
    const rawTotal = firstValue(infoOf(payload), ["total", "totalCount", "total_count", "count", "recordCount"]);
    const total = rawTotal === "" ? null : Number(rawTotal);
    if (rows.length < data.pageSize || Number.isFinite(total) && orders.length >= total) break;
  }
  return orders;
}

function detailList(payload) {
  const info = infoOf(payload);
  if (Array.isArray(info)) return info;
  return listFromInfo(info, ["list", "data", "records", "rows", "orderList", "order_list", "orders", "orderInfoList"]);
}

async function loadOrderDetails(orders) {
  const batches = [];
  for (let index = 0; index < orders.length; index += 30) {
    batches.push(orders.slice(index, index + 30));
  }
  let failures = 0;
  const results = await Promise.all(batches.map(async function (batch) {
    try {
      const payload = await post("order/detail", { orderNoList: batch.map(orderNumber) });
      return detailList(payload);
    } catch (_) {
      failures += 1;
      return [];
    }
  }));
  const details = new Map();
  results.flat().forEach(function (detail) {
    const number = orderNumber(detail);
    if (number) details.set(number, detail);
  });
  return {
    failures: failures,
    orders: orders.map(function (order) {
      return Object.assign({}, order, details.get(orderNumber(order)) || {});
    })
  };
}

function detailFrom(payload) {
  const info = infoOf(payload);
  if (Array.isArray(info)) return info[0] || null;
  const list = listFromInfo(info, ["list", "data", "records", "rows", "orderList", "order_list", "orders", "orderInfoList"]);
  return list[0] || info || null;
}

function orderDetail(order) {
  return order && order.detail && typeof order.detail === "object" ? order.detail : order;
}

function goodsFrom(order) {
  order = orderDetail(order);
  if (!order || typeof order !== "object") return [];
  const keys = ["orderGoodsInfoList", "goodsList", "orderGoodsList", "productList", "goodsInfoList"];
  for (let index = 0; index < keys.length; index += 1) {
    if (Array.isArray(order[keys[index]])) return order[keys[index]];
  }
  return [];
}

function goodsLines(order) {
  const lines = new Map();
  goodsFrom(order).forEach(function (goods) {
    const sku = display(
      goods.sellerSku || goods.skuCode || goods.sku || goods.goodsSn || goods.goodsId,
      "SKU待返回"
    );
    const quantity = Number(goods.quantity || goods.goodsQuantity || goods.qty || 1);
    const current = lines.get(sku) || 0;
    lines.set(sku, current + (Number.isFinite(quantity) && quantity > 0 ? quantity : 1));
  });
  return Array.from(lines, function (entry) { return { sku: entry[0], quantity: entry[1] }; });
}

function warehouseGoodsLines(order) {
  const lines = new Map();
  const goods = Array.isArray(order && order.goods) ? order.goods : [];
  goods.forEach(function (item) {
    const warehouseSKU = item.warehouse_sku || "";
    const sourceSKU = item.sku_code || item.seller_sku || item.goods_sn || item.goods_id || "";
    const key = warehouseSKU || "__unmapped__:" + sourceSKU;
    const mappedQuantity = Number(item.warehouse_quantity || 1);
    const quantity = Number.isFinite(mappedQuantity) && mappedQuantity > 0 ? mappedQuantity : 1;
    const current = lines.get(key) || { sku: warehouseSKU || "SKU待映射", quantity: 0 };
    current.quantity += quantity;
    lines.set(key, current);
  });
  return Array.from(lines.values());
}

function quantityText(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return "0";
  if (Number.isInteger(number)) return String(number);
  return number.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
}

function orderNumber(order) {
  if (order && order.order_no) return String(order.order_no);
  order = orderDetail(order);
  return display(firstValue(order, [
    "orderNo", "order_no", "orderSn", "order_sn", "orderId", "order_id",
    "billno", "billNo", "orderCode", "order_code", "parentOrderNo"
  ]), "");
}

function orderStatus(order) {
  if (order && order.order_status != null) return order.order_status;
  order = orderDetail(order);
  return firstValue(order, ["orderStatus", "order_status", "status", "newGoodsStatus"]);
}

function orderTime(order) {
  order = orderDetail(order);
  return firstValue(order, [
    "updateTime", "updatedTime", "orderUpdateTime", "order_updated_at",
    "orderMsgUpdateTime", "orderAllocateTime", "createTime", "createdTime", "orderCreateTime"
  ]);
}

function orderDeadline(order) {
  order = orderDetail(order);
  return firstValue(order, ["needDeliveryTime", "need_delivery_time"]);
}

function parseOrderTime(value) {
  if (!value) return null;
  const normalized = String(value).replace(/([+-]\d{2})(\d{2})$/, "$1:$2");
  const parsed = new Date(normalized);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

function shortTime(value) {
  const parsed = parseOrderTime(value);
  if (!parsed) return "未返回";
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hour12: false
  }).format(parsed);
}

function relativeDeadline(value) {
  const parsed = parseOrderTime(value);
  if (!parsed) return "未返回";
  const minutes = Math.floor((parsed.getTime() - Date.now()) / 60000);
  if (minutes <= 0) return "已超时";
  const hours = Math.floor(minutes / 60);
  return hours >= 24 ? Math.floor(hours / 24) + " 天 " + hours % 24 + " 小时" : hours + " 小时";
}

function logisticsOptions(order) {
  order = orderDetail(order);
  for (const key of ["optionalLogisticsList", "optional_logistics_list"]) {
    if (Array.isArray(order && order[key])) return order[key].map(Number);
  }
  return [];
}

function supportsIntegratedLogistics(order) {
  order = orderDetail(order);
  const options = logisticsOptions(order);
  if (options.length) return options.includes(1);
  return Number(firstValue(order, ["performanceType", "performance_type"])) === 1;
}

function fulfillmentTypeLabel(order) {
  order = orderDetail(order);
  const options = logisticsOptions(order);
  if (options.includes(1) && options.includes(2)) return "集成 / 自发货可选";
  if (!supportsIntegratedLogistics(order)) return "商家自发货";
  const placeType = Number(firstValue(order, ["orderPlaceType", "order_place_type"]));
  if (placeType === 1) return "集成物流 · 平台指定";
  if (placeType === 2) return "集成物流 · 自主选择";
  return "SHEIN 集成物流";
}

function unprocessableReason(order) {
  order = orderDetail(order);
  if (String(firstValue(order, ["orderType", "order_type"])) === "5") return "认证仓订单不可在线履约";
  const printStatus = String(firstValue(order, ["printOrderStatus", "print_order_status"]));
  if (!printStatus || printStatus === "1") return "";
  const reasons = order.unProcessReason || order.un_process_reason;
  const labels = {
    "1": "平台处理中", "2": "客服验证中", "3": "待同步发票", "4": "包裹尚未生成",
    "5": "平台处理中", "6": "部分商品缺货", "7": "平台处理中", "8": "平台处理中",
    "9": "部分商品缺货", "10": "尚未设置卖家仓库", "11": "尚未设置卖家仓库", "12": "需要确认拆包",
    "13": "CTE 发票开票中"
  };
  if (Array.isArray(reasons) && reasons.length) {
    return reasons.map(function (reason) { return labels[String(reason)] || "问题代码 " + reason; }).join("；");
  }
  return "平台标记订单暂不可处理";
}

function orderIsActionable(order) {
  const status = String(orderStatus(order));
  if (status === "1") return !unprocessableReason(order);
  return status === "2" && supportsIntegratedLogistics(order);
}

function statusLabel(status) {
  const labels = { "1": "待处理", "2": "待发货", "4": "已发货", "5": "已签收", "6": "已退款", "7": "待揽收", "8": "已报损", "9": "已拒收" };
  return labels[String(status)] || display(status, "未知");
}

function statusClass(status) {
  const value = String(status);
  if (value === "4" || value === "5") return "good";
  if (value === "6" || value === "8" || value === "9") return "error";
  if (value === "7") return "info";
  return "pending";
}

function formatChinaTime(date) {
  const parts = new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false
  }).formatToParts(date);
  const result = {};
  parts.forEach(function (part) { result[part.type] = part.value; });
  return result.year + "-" + result.month + "-" + result.day + " " + result.hour + ":" + result.minute + ":" + result.second;
}

function showResult(title, payload) {
  byId("result-title").textContent = title;
  byId("result-json").textContent = JSON.stringify(payload, null, 2);
  const links = [];
  function walk(value, path) {
    if (Array.isArray(value)) {
      value.forEach(function (item, index) { walk(item, path + "[" + index + "]"); });
      return;
    }
    if (!value || typeof value !== "object") return;
    Object.keys(value).forEach(function (key) {
      const item = value[key];
      const nextPath = path ? path + "." + key : key;
      if (typeof item === "string" && /^https:\/\//i.test(item) && /(url|download|label|file|express)/i.test(key)) {
        links.push({ label: nextPath, url: item });
      } else {
        walk(item, nextPath);
      }
    });
  }
  walk(payload, "");
  byId("result-links").innerHTML = links.map(function (link) {
    return '<a class="result-link" href="' + escapeHTML(link.url) + '" target="_blank" rel="noreferrer"><span>' +
      escapeHTML(link.label) + '</span><strong>打开</strong></a>';
  }).join("");
  byId("result-drawer").classList.add("open");
  byId("result-drawer").setAttribute("aria-hidden", "false");
}

function closeResult() {
  byId("result-drawer").classList.remove("open");
  byId("result-drawer").setAttribute("aria-hidden", "true");
}

function selectView(name) {
  const titles = {
    orders: "待发货订单",
    processing: "自动处理中",
    exceptions: "自动发货异常",
    manual: "人工订单",
    "oms-statuses": "领星订单状态",
    ledger: "自动发货账本",
    labels: "面单任务",
    tools: "物流工具"
  };
  document.querySelectorAll(".nav-button").forEach(function (button) {
    button.classList.toggle("active", button.dataset.view === name);
  });
  document.querySelectorAll(".view").forEach(function (view) {
    view.classList.toggle("active", view.id === "view-" + name);
  });
  byId("crumb-current").textContent = titles[name] || "";
  byId("sidebar").classList.remove("open");
  byId("backdrop").classList.remove("visible");
  if (name === "processing") loadJobQueue("processing");
  if (name === "exceptions") loadJobQueue("exceptions");
  if (name === "manual") loadManualOrders();
  if (name === "ledger") loadJobQueue("all");
  if (name === "labels") loadTasks();
}

async function loadTasks(button) {
  await busy(button || null, async function () {
    try {
      const payload = await request("shipping/tasks");
      state.tasks = Array.isArray(payload.data) ? payload.data : [];
      renderTasks();
    } catch (error) {
      toast(error.message, true);
    }
  });
}

function formatTaskTime(value) {
  if (!value) return "-";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? display(value) : formatChinaTime(parsed);
}

function taskStatusClass(status) {
  if (status === "label_ready") return "good";
  if (status === "ready") return "info";
  if (status === "failed") return "error";
  return "pending";
}

function renderTasks() {
  const rows = byId("task-rows");
  const table = rows.closest(".table-shell");
  table.classList.toggle("is-empty", state.tasks.length === 0);
  rows.innerHTML = state.tasks.map(function (task, index) {
    const identifier = task.place_request_id || task.package_no || task.waybill_no;
    const canCheck = Boolean(task.place_request_id || task.delivery_no);
    const platformLabel = Number(task.order_place_type) === 1 && task.order_no && task.package_no;
    const canPrint = Boolean(platformLabel || (task.status === "ready" || task.status === "label_ready") &&
      (task.delivery_no || task.order_no && task.package_no));
    const failureTitle = task.failure_reason ? ' title="' + escapeHTML(task.failure_reason) + '"' : "";
    return "<tr><td><strong>" + escapeHTML(task.order_no) + "</strong></td><td>" +
      escapeHTML(display(task.express_channel_code)) + "</td><td>" + escapeHTML(display(identifier)) +
      "</td><td>" + escapeHTML(display(task.delivery_no)) + "</td><td><span class=\"badge " +
      taskStatusClass(task.status) + "\"" + failureTitle + ">" + escapeHTML(taskStatusLabel(task.status)) +
      "</span></td><td>" + escapeHTML(formatTaskTime(task.updated_at)) +
      "</td><td><div class=\"action-row\"><button class=\"table-action\" data-check-task=\"" + index +
      "\" " + (canCheck ? "" : "disabled") + ">查询结果</button><button class=\"table-action primary\" data-label-task=\"" + index +
      "\" " + (canPrint ? "" : "disabled") + ">获取面单</button></div></td></tr>";
  }).join("");
  byId("nav-label-count").textContent = String(state.tasks.length);
  byId("metric-task-total").textContent = String(state.tasks.length);
  byId("metric-task-placed").textContent = String(state.tasks.filter(function (task) { return task.status === "placed"; }).length);
  byId("metric-task-checked").textContent = String(state.tasks.filter(function (task) {
    return ["checking", "confirming", "ready"].includes(task.status);
  }).length);
  byId("metric-task-label").textContent = String(state.tasks.filter(function (task) { return task.status === "label_ready"; }).length);
}

function taskStatusLabel(status) {
  return {
    discovered: "历史包裹",
    placed: "下单已提交",
    checking: "查询中",
    confirming: "承运商确认中",
    ready: "可获取面单",
    failed: "下单失败",
    label_ready: "面单已生成"
  }[status] || status;
}

async function checkTask(index, button) {
  const task = state.tasks[index];
  if (!task) return;
  await busy(button, async function () {
    try {
      const data = task.place_request_id
        ? { placeRequestId: task.place_request_id }
        : { deliveryNo: task.delivery_no };
      const payload = await post("shipping/check", data);
      await loadTasks();
      showResult("订单 " + task.order_no + " 下单结果", payload);
    } catch (error) {
      toast(error.message, true);
    }
  });
}

async function fetchLabel(index, button) {
  const task = state.tasks[index];
  if (!task) return;
  if (!window.confirm("确认向 SHEIN 获取订单 " + task.order_no + " 的面单？")) return;
  await busy(button, async function () {
    try {
      let data;
      if (Number(task.order_place_type) === 1 || !task.delivery_no) {
        data = { orderNo: task.order_no, packageNo: [task.package_no] };
      } else {
        data = { deliveryNo: task.delivery_no };
      }
      const reference = (data.deliveryNo || task.order_no + ":" + task.package_no) +
        ":" + Math.floor(Date.now() / 3600000);
      const payload = await sensitivePost("shipping/label", data, "print-express-info", reference);
      await loadTasks();
      showResult("订单 " + task.order_no + " 面单", payload);
      toast(payload.cached ? "已返回此前生成的面单" : "面单已获取");
    } catch (error) {
      toast(error.message, true);
    }
  });
}

function renderOrders(rows) {
  state.orders = rows;
  state.orderPage = 1;
  filterOrders();
}

function orderAction(order, number) {
  const status = String(orderStatus(order));
  const automatic = '<button class="table-action primary" data-auto-order="' + escapeHTML(number) + '">自动发货</button>';
  const oms = '<button class="table-action" data-oms-order="' + escapeHTML(number) + '">查领星</button>';
  if (status === "1") {
    const reason = unprocessableReason(order);
    return '<div class="action-row">' + automatic + '<button class="table-action" data-fulfill-order="' + escapeHTML(number) + '"' +
      (reason ? ' disabled title="' + escapeHTML(reason) + '"' : "") + ">" +
      (reason ? "暂不可处理" : "人工发货") + "</button>" + oms + "</div>";
  }
  if (status === "2") {
    const integrated = supportsIntegratedLogistics(order);
    return '<div class="action-row">' + automatic + '<button class="table-action" data-fulfill-order="' + escapeHTML(number) + '"' +
      (integrated ? "" : ' disabled title="订单仅支持商家自发货"') + ">" +
      (integrated ? "人工发货" : "仅自发货") + "</button>" + oms + "</div>";
  }
  return oms;
}

function workflowStatusText(order) {
  const status = String(orderStatus(order));
  if (status === "1") return unprocessableReason(order) ? "平台暂不可处理" : "待流转";
  if (status === "2") return supportsIntegratedLogistics(order) ? "待选择物流" : "待商家自发货";
  return statusLabel(status);
}

function renderOrderPager(total) {
  const pager = byId("orders-pager");
  const pageCount = Math.max(1, Math.ceil(total / state.pageSize));
  state.orderPage = Math.min(Math.max(1, state.orderPage), pageCount);
  pager.hidden = total <= state.pageSize;
  pager.innerHTML = '<button type="button" data-order-page="' + (state.orderPage - 1) + '" title="上一页" aria-label="上一页" ' +
    (state.orderPage <= 1 ? "disabled" : "") + '><svg><use href="#i-chevron"/></svg></button>' +
    '<span>第 ' + state.orderPage + " / " + pageCount + " 页 · 共 " + total + " 条</span>" +
    '<button type="button" class="next" data-order-page="' + (state.orderPage + 1) + '" title="下一页" aria-label="下一页" ' +
    (state.orderPage >= pageCount ? "disabled" : "") + '><svg><use href="#i-chevron"/></svg></button>';
}

function filterOrders() {
  const query = byId("order-search").value.trim().toLowerCase();
  const filtered = state.orders.filter(function (order) {
    if (!query) return true;
    const goods = Array.isArray(order.goods) ? order.goods : [];
    const text = [
      orderNumber(order), firstValue(order, ["salesSite", "site"]), orderStatus(order),
      warehouseGoodsLines(order).map(function (line) { return line.sku; }).join(" "),
      goods.map(function (item) { return [item.sku_code, item.seller_sku, item.goods_sn].join(" "); }).join(" ")
    ].join(" ").toLowerCase();
    return text.includes(query);
  });
  const pageCount = Math.max(1, Math.ceil(filtered.length / state.pageSize));
  state.orderPage = Math.min(state.orderPage, pageCount);
  const start = (state.orderPage - 1) * state.pageSize;
  const items = filtered.slice(start, start + state.pageSize);
  const rows = byId("order-rows");
  const table = rows.closest(".table-shell");
  table.classList.toggle("is-empty", items.length === 0);
  rows.innerHTML = items.map(function (order) {
    const number = orderNumber(order);
    const lineHTML = warehouseGoodsLines(order).map(function (line) {
      return '<span class="sku-line"><b>' + escapeHTML(line.sku) +
        '</b><span>× ' + escapeHTML(quantityText(line.quantity)) + "</span></span>";
    }).join("");
    const status = orderStatus(order);
    const deadline = orderDeadline(order);
    const tone = String(status) === "2" ? "info" : "pending";
    return '<tr><td><div class="order-id"><button class="order-link" data-detail-order="' + escapeHTML(number) + '">' +
      escapeHTML(number) + "</button><small>更新 " + escapeHTML(shortTime(orderTime(order))) +
      '</small></div></td><td><div class="sku-stack">' + (lineHTML || "-") +
      '</div></td><td><div class="order-id"><strong>' + escapeHTML(relativeDeadline(deadline)) +
      "</strong><small>" + escapeHTML(shortTime(deadline)) +
      '</small></div></td><td>' + escapeHTML(supportsIntegratedLogistics(order) ? "SHEIN集成物流" : "商家自发货") +
      '</td><td><span class="badge ' + tone + '">' + escapeHTML(workflowStatusText(order)) +
      "</span></td><td>" + orderAction(order, number) + "</td></tr>";
  }).join("");
  byId("order-total").textContent = "共 " + filtered.length + " 条";
  byId("metric-orders").textContent = String(filtered.length);
  const units = filtered.reduce(function (total, order) {
    return total + warehouseGoodsLines(order).reduce(function (sum, line) { return sum + line.quantity; }, 0);
  }, 0);
  byId("metric-units").textContent = quantityText(units);
  byId("metric-actionable").textContent = String(filtered.length);
  byId("nav-order-count").textContent = String(filtered.length);
  renderBulkBatch();
  renderOrderPager(filtered.length);
}

async function loadOrders(button) {
  await busy(button, async function () {
    try {
      const payload = button
        ? await request("fulfillment/orders/sync", { method: "POST" })
        : await request("fulfillment/orders?queue=pending");
      const data = payload.data || {};
      const orders = Array.isArray(data) ? data : (Array.isArray(data.pending) ? data.pending : []);
      renderOrders(orders);
      if (data.manual_count != null) byId("nav-manual-count").textContent = String(data.manual_count);
      if (button) byId("metric-sync").textContent = shortTime(new Date());
    } catch (error) {
      toast(error.message, true);
    }
  });
}

async function runAutoOrders(orderNos, button) {
  const values = Array.from(new Set((orderNos || []).filter(Boolean)));
  if (!values.length) {
    toast("当前没有可自动发货订单", true);
    return;
  }
  if (!window.confirm("确认将 " + values.length + " 个订单加入自动发货？系统将自动流转订单、匹配最低价物流并购买面单。")) return;
  await busy(button || null, async function () {
    try {
      const payload = await request("auto-fulfillment/run", {
        method: "POST",
        headers: { "X-Confirm-Shein-Action": "auto-fulfillment" },
        body: JSON.stringify({ order_nos: values, confirm: true })
      });
      const data = payload.data || {};
      const queued = data.batch && data.batch.total_orders
        ? data.batch.total_orders
        : (Array.isArray(data.queued) ? data.queued.length : 0);
      toast("已加入 " + queued + " 个自动发货任务");
      state.bulkBatch = data.batch || state.bulkBatch;
      renderBulkBatch();
      await Promise.all([loadOrders(), loadJobQueue("processing")]);
      selectView("processing");
    } catch (error) {
      toast(error.message, true);
    }
  });
  loadBulkBatch();
}

async function loadBulkBatch() {
  try {
    const payload = await request("auto-fulfillment/batches/latest");
    const batch = payload.data || {};
    state.bulkBatch = batch.id ? batch : null;
    renderBulkBatch();
  } catch (error) {
    toast(error.message, true);
  }
}

function renderBulkBatch() {
  const batch = state.bulkBatch;
  const node = byId("batch-state");
  const button = byId("run-all-auto");
  if (!batch) {
    node.hidden = true;
    button.disabled = state.orders.length === 0;
    button.innerHTML = '<svg><use href="#i-truck"/></svg>一键发货';
    return;
  }
  const progress = batch.succeeded_orders + " / " + batch.total_orders;
  node.hidden = false;
  node.className = "batch-state" +
    (batch.status === "stopped" ? " error" : batch.status === "completed" ? " good" : "");
  if (batch.status === "running") {
    node.textContent = "一键发货进行中 · 已完成 " + progress;
    button.disabled = true;
    button.innerHTML = '<svg><use href="#i-truck"/></svg>执行中 ' + progress;
  } else if (batch.status === "stopped") {
    node.textContent = "批次已在首个异常处停止 · 已完成 " + progress;
    button.disabled = state.orders.length === 0;
    button.innerHTML = '<svg><use href="#i-truck"/></svg>新建一键发货';
  } else {
    node.textContent = "最近批次已完成 · " + progress;
    button.disabled = state.orders.length === 0;
    button.innerHTML = '<svg><use href="#i-truck"/></svg>一键发货';
  }
}

function jobStepLabel(step) {
  return {
    queued: "等待执行",
    validating: "校验订单",
    transition_order: "流转待发货",
    query_warehouses: "查询可用仓",
    quote_channels: "匹配最低价物流",
    place_order: "在线下单",
    check_order: "等待下单结果",
    print_label: "获取面单",
    completed: "自动发货完成",
    failed: "自动发货停止"
  }[step] || display(step);
}

function autoStatusLabel(status) {
  return {
    queued: "排队中",
    running: "执行中",
    waiting_confirmation: "等待承运商",
    failed: "异常",
    completed: "已完成"
  }[status] || display(status);
}

function autoStatusClass(status) {
  if (status === "completed") return "good";
  if (status === "failed") return "error";
  if (status === "waiting_confirmation") return "info";
  return "pending";
}

async function loadJobQueue(queue, button) {
  await busy(button || null, async function () {
    try {
      const payload = await request("auto-fulfillment/jobs?queue=" + encodeURIComponent(queue));
      const jobs = Array.isArray(payload.data) ? payload.data : [];
      if (queue === "processing") {
        state.processingJobs = jobs;
        renderProcessingJobs();
      } else if (queue === "exceptions") {
        state.exceptionJobs = jobs;
        renderExceptionJobs();
      } else {
        state.ledgerJobs = jobs;
        renderLedgerJobs();
      }
    } catch (error) {
      toast(error.message, true);
    }
  });
}

function jobWarehouseChannel(job) {
  return '<div class="order-id"><strong>' + escapeHTML(display(job.warehouse_address_code, "尚未选仓")) +
    '</strong><small>' + escapeHTML(display(job.express_channel_code, "尚未选渠道")) + "</small></div>";
}

function renderProcessingJobs() {
  const jobs = state.processingJobs;
  const rows = byId("processing-rows");
  rows.closest(".table-shell").classList.toggle("is-empty", jobs.length === 0);
  rows.innerHTML = jobs.map(function (job) {
    return '<tr><td><strong>' + escapeHTML(job.order_no) +
      '</strong></td><td><span class="badge ' + autoStatusClass(job.status) + '">' +
      escapeHTML(jobStepLabel(job.current_step)) + "</span></td><td>" + jobWarehouseChannel(job) +
      '</td><td>' + escapeHTML(job.performance_cost ? job.performance_cost + " " + display(job.currency_code, "") : "-") +
      '</td><td>' + escapeHTML(display(job.attempts, "0")) +
      '</td><td>' + escapeHTML(formatTaskTime(job.updated_at)) + "</td></tr>";
  }).join("");
  byId("metric-processing-total").textContent = String(jobs.length);
  byId("metric-processing-queued").textContent = String(jobs.filter(function (job) { return job.status === "queued"; }).length);
  byId("metric-processing-running").textContent = String(jobs.filter(function (job) { return job.status === "running"; }).length);
  byId("metric-processing-waiting").textContent = String(jobs.filter(function (job) { return job.status === "waiting_confirmation"; }).length);
  byId("nav-processing-count").textContent = String(jobs.length);
}

function renderExceptionJobs() {
  const jobs = state.exceptionJobs;
  const rows = byId("exception-rows");
  rows.closest(".table-shell").classList.toggle("is-empty", jobs.length === 0);
  rows.innerHTML = jobs.map(function (job, index) {
    return '<tr><td><strong>' + escapeHTML(job.order_no) +
      '</strong></td><td>' + escapeHTML(jobStepLabel(job.current_step)) +
      '</td><td><span class="badge error">' + escapeHTML(display(job.error_code, "workflow_error")) +
      '</span></td><td><div class="error-copy">' + escapeHTML(display(job.error_message, "自动履约失败")) +
      '</div></td><td>' + escapeHTML(formatTaskTime(job.updated_at)) +
      '</td><td><button class="table-action primary" data-retry-job="' + index + '">重试</button></td></tr>';
  }).join("");
  byId("metric-exception-total").textContent = String(jobs.length);
  byId("metric-exception-api").textContent = String(jobs.filter(function (job) {
    return job.error_code && !["workflow_error", "timeout", "queue_full"].includes(job.error_code);
  }).length);
  byId("metric-exception-workflow").textContent = String(jobs.filter(function (job) {
    return ["workflow_error", "queue_full"].includes(job.error_code);
  }).length);
  byId("metric-exception-timeout").textContent = String(jobs.filter(function (job) { return job.error_code === "timeout"; }).length);
  byId("nav-exception-count").textContent = String(jobs.length);
}

function renderLedgerJobs() {
  const jobs = state.ledgerJobs;
  const rows = byId("ledger-rows");
  rows.closest(".table-shell").classList.toggle("is-empty", jobs.length === 0);
  rows.innerHTML = jobs.map(function (job) {
    const identifier = job.delivery_no || job.place_request_id || "-";
    return '<tr><td><strong>' + escapeHTML(job.order_no) +
      '</strong></td><td><span class="badge ' + autoStatusClass(job.status) + '">' +
      escapeHTML(autoStatusLabel(job.status)) + "</span></td><td>" + jobWarehouseChannel(job) +
      '</td><td>' + escapeHTML(job.performance_cost ? job.performance_cost + " " + display(job.currency_code, "") : "-") +
      '</td><td>' + escapeHTML(identifier) +
      '</td><td>' + escapeHTML(formatTaskTime(job.updated_at)) + "</td></tr>";
  }).join("");
  const processing = jobs.filter(function (job) {
    return ["queued", "running", "waiting_confirmation"].includes(job.status);
  }).length;
  byId("metric-ledger-total").textContent = String(jobs.length);
  byId("metric-ledger-processing").textContent = String(processing);
  byId("metric-ledger-failed").textContent = String(jobs.filter(function (job) { return job.status === "failed"; }).length);
  byId("metric-ledger-completed").textContent = String(jobs.filter(function (job) { return job.status === "completed"; }).length);
  byId("nav-ledger-count").textContent = String(jobs.length);
}

async function loadManualOrders(button) {
  await busy(button || null, async function () {
    try {
      const payload = await request("fulfillment/orders?queue=manual");
      state.manualOrders = Array.isArray(payload.data) ? payload.data : [];
      renderManualOrders();
    } catch (error) {
      toast(error.message, true);
    }
  });
}

function renderManualOrders() {
  const query = byId("manual-search").value.trim().toLowerCase();
  const filtered = state.manualOrders.filter(function (order) {
    const goods = Array.isArray(order.goods) ? order.goods : [];
    const text = [order.order_no, order.manual_reasons, goods.map(function (goodsItem) {
      return [goodsItem.sku_code, goodsItem.seller_sku, goodsItem.title].join(" ");
    }).join(" ")].join(" ").toLowerCase();
    return !query || text.includes(query);
  });
  const rows = byId("manual-rows");
  rows.closest(".table-shell").classList.toggle("is-empty", filtered.length === 0);
  rows.innerHTML = filtered.map(function (order) {
    const goods = Array.isArray(order.goods) ? order.goods : [];
    const goodsHTML = goods.map(function (goodsItem) {
      return '<span class="sku-line"><b>' + escapeHTML(display(goodsItem.sku_code || goodsItem.seller_sku)) +
        '</b><span>' + escapeHTML(display(goodsItem.title, "商品")) + "</span></span>";
    }).join("");
    const reasons = (order.manual_reasons || []).map(function (reason) {
      return '<span class="reason-line">' + escapeHTML(reason) + "</span>";
    }).join("");
    const status = String(order.order_status);
    const blocked = unprocessableReason(order);
    let action;
    if (status === "1") {
      action = '<button class="table-action primary" data-manual-fulfill="' + escapeHTML(order.order_no) + '"' +
        (blocked ? ' disabled title="' + escapeHTML(blocked) + '"' : "") + ">" +
        (blocked ? "暂不可处理" : "人工发货") + "</button>";
    } else {
      const integrated = supportsIntegratedLogistics(order);
      action = '<button class="table-action primary" data-manual-fulfill="' + escapeHTML(order.order_no) + '"' +
        (integrated ? "" : ' disabled title="订单仅支持商家自发货"') + ">" +
        (integrated ? "人工发货" : "仅自发货") + "</button>";
    }
    action = '<div class="action-row">' + action + '<button class="table-action" data-oms-order="' + escapeHTML(order.order_no) + '">查领星</button></div>';
    return '<tr><td><button class="order-link" data-manual-detail="' + escapeHTML(order.order_no) + '">' +
      escapeHTML(order.order_no) + '</button></td><td><div class="sku-stack">' + (goodsHTML || "-") +
      '</div></td><td><div class="order-id"><strong>' + escapeHTML(relativeDeadline(orderDeadline(order))) +
      '</strong><small>' + escapeHTML(shortTime(orderDeadline(order))) +
      '</small></div></td><td><div class="reason-stack">' + reasons +
      '</div></td><td><span class="badge pending">' + escapeHTML(workflowStatusText(order)) +
      "</span></td><td>" + action + "</td></tr>";
  }).join("");
  byId("manual-total").textContent = "共 " + filtered.length + " 条";
  byId("metric-manual-total").textContent = String(state.manualOrders.length);
  byId("metric-manual-multi").textContent = String(state.manualOrders.filter(function (order) {
    return (order.manual_reasons || []).some(function (reason) { return reason.includes("多件"); });
  }).length);
  byId("metric-manual-sku").textContent = String(state.manualOrders.filter(function (order) {
    return (order.manual_reasons || []).some(function (reason) { return reason.includes("SKU"); });
  }).length);
  byId("metric-manual-other").textContent = String(state.manualOrders.filter(function (order) {
    return !(order.manual_reasons || []).some(function (reason) { return reason.includes("多件") || reason.includes("SKU"); });
  }).length);
  byId("nav-manual-count").textContent = String(state.manualOrders.length);
}

async function transitionFulfillmentOrder(button) {
  const orderNo = state.orderNo;
  if (!window.confirm("确认将订单 " + orderNo + " 流转到待发货？完成后将在当前弹窗继续选择仓库和物流。")) return;
  await busy(button, async function () {
    try {
      const detailPayload = await post("order/detail", { orderNoList: [orderNo] });
      const detail = detailFrom(detailPayload) || {};
      const status = String(orderStatus(detail));
      if (status === "2") {
        toast("订单已经处于待发货状态");
        await loadFulfillmentContext();
        return;
      }
      if (status !== "1") throw new Error("订单当前不是待处理状态，请刷新后重试");
      if (String(firstValue(detail, ["orderType", "order_type"])) === "5") {
        throw new Error("认证仓订单不能导出地址或在线履约");
      }
      const printStatus = String(firstValue(detail, ["printOrderStatus", "print_order_status"]));
      if (printStatus && printStatus !== "1") {
        throw new Error(display(
          firstValue(detail, ["unProcessReason", "un_process_reason"]),
          "平台标记订单暂不可处理"
        ));
      }
      await sensitivePost(
        "order/export-address",
        { orderNo: orderNo, handleType: 2 },
        "export-address-transition",
        orderNo
      );
      loadOrders();
      loadManualOrders();
      if (state.orderNo !== orderNo) return;
      let ready = false;
      for (let attempt = 0; attempt < 3; attempt += 1) {
        await new Promise(function (resolve) {
          window.setTimeout(resolve, attempt === 0 ? 800 : 1200);
        });
        if (state.orderNo !== orderNo) return;
        const currentStatus = await loadFulfillmentContext();
        if (currentStatus === "2") {
          ready = true;
          break;
        }
      }
      toast(ready ? "订单已流转到待发货" : "流转已提交，平台状态同步中，可在弹窗内刷新");
    } catch (error) {
      toast(error.message, true);
    }
  });
}

async function showOrderDetail(orderNo) {
  try {
    const payload = await post("order/detail", { orderNoList: [orderNo] });
    await loadTasks();
    showResult("订单 " + orderNo + " 详情", payload);
  } catch (error) {
    toast(error.message, true);
  }
}

function renderOrderSummary(detail) {
  const goods = goodsFrom(detail);
  const quantity = goods.reduce(function (total, item) {
    return total + Number(item.quantity || item.goodsQuantity || item.qty || 1);
  }, 0);
  const status = detail && (detail.orderStatus != null ? detail.orderStatus : detail.newGoodsStatus);
  const values = [
    ["订单号", state.orderNo],
    ["订单状态", statusLabel(status)],
    ["商品明细", goods.length ? goods.length + " 个 / " + quantity + " 件" : "待接口返回"],
    ["销售站点", detail && firstValue(detail, ["salesSite", "site"])]
  ];
  byId("order-summary").innerHTML = values.map(function (item) {
    return "<div><small>" + escapeHTML(item[0]) + "</small><strong>" + escapeHTML(display(item[1])) + "</strong></div>";
  }).join("");
}

function warehouseList(payload) {
  const info = infoOf(payload);
  const result = listFromInfo(info, ["availableWarehouses", "warehouseList", "warehouses", "list"]);
  warehouseList.current = result;
  return result;
}

function renderWarehouses(warehouses) {
  state.warehouse = null;
  state.channel = null;
  state.preRequestId = "";
  renderChannels([]);
  byId("warehouse-choices").innerHTML = warehouses.map(function (warehouse, index) {
    const code = warehouse.warehouseAddressCode || warehouse.warehouseCode || "";
    const available = warehouse.availableStatus == null || Number(warehouse.availableStatus) === 1;
    const reason = warehouse.unavailableReason || warehouse.reason || (available ? "当前订单可用" : "当前不可用");
    return '<label class="choice ' + (available ? "" : "disabled") + '"><input type="radio" name="warehouse" value="' +
      escapeHTML(code) + '" data-warehouse-index="' + index + '" ' + (available ? "" : "disabled") +
      '><span><strong>' + escapeHTML(warehouse.warehouseName || code) + '</strong><small>' +
      escapeHTML(code + " · " + reason) + '</small></span></label>';
  }).join("") || '<div class="empty-inline">当前订单没有可用发货仓</div>';
  updatePurchaseSummary();
}

function setFulfillmentTransitionRequired(required) {
  byId("fulfillment-transition").hidden = !required;
  ["warehouse-section", "package-section", "purchase-section"].forEach(function (id) {
    byId(id).hidden = required;
  });
}

async function loadFulfillmentContext() {
  setFulfillmentTransitionRequired(false);
  byId("warehouse-choices").innerHTML = '<div class="loading-line">正在查询订单与可用仓库</div>';
  try {
    const detailPayload = await post("order/detail", { orderNoList: [state.orderNo] });
    state.detail = detailFrom(detailPayload);
    renderOrderSummary(state.detail || {});
    const status = String(orderStatus(state.detail || {}));
    if (status === "1") {
      setFulfillmentTransitionRequired(true);
      renderChannels([]);
      updatePurchaseSummary();
      return status;
    }
    if (status !== "2") {
      throw new Error("订单当前不在可人工履约状态，请刷新订单后重试");
    }
    const warehousePayload = await post("shipping/warehouses", { orderNo: state.orderNo });
    renderWarehouses(warehouseList(warehousePayload));
    await loadTasks();
    return status;
  } catch (error) {
    byId("warehouse-choices").innerHTML = '<div class="empty-inline">查询失败，请刷新重试</div>';
    toast(error.message, true);
    return "";
  }
}

function openFulfillment(orderNo) {
  state.orderNo = orderNo;
  state.detail = null;
  state.warehouse = null;
  state.channel = null;
  state.preRequestId = "";
  const queuedOrder = state.orders.concat(state.manualOrders).find(function (order) {
    return order.order_no === orderNo;
  });
  byId("package-length").value = "20";
  byId("package-width").value = "15";
  byId("package-height").value = "5";
  byId("package-weight").value = "300";
  const spec = queuedOrder && queuedOrder.package_spec;
  if (spec) {
    byId("package-length").value = display(spec.length_cm, "20");
    byId("package-width").value = display(spec.width_cm, "15");
    byId("package-height").value = display(spec.height_cm, "5");
    const weight = Number(spec.weight_kg);
    byId("package-weight").value = Number.isFinite(weight) && weight > 0 ? String(weight * 1000) : "300";
  }
  byId("fulfillment-title").textContent = "订单 " + orderNo + " 发货";
  setFulfillmentTransitionRequired(false);
  renderOrderSummary({});
  renderChannels([]);
  updatePurchaseSummary();
  const dialog = byId("fulfillment-dialog");
  if (!dialog.open) dialog.showModal();
  loadFulfillmentContext();
  loadXLWMSWarehousePreview(orderNo);
}

function channelList(payload) {
  return listFromInfo(infoOf(payload), ["channelInfoList", "channels", "channelList", "list"]);
}

function renderChannels(channels) {
  state.channel = null;
  byId("channel-choices").innerHTML = channels.map(function (channel, index) {
    const code = channel.expressChannelCode || channel.channelCode || "";
    const name = channel.expressShortName || channel.expressName || channel.expressIdCode || code;
    const cost = channel.performanceCost != null ? channel.performanceCost : channel.shippingFee;
    const currency = channel.currencyCode || channel.currency || "";
    const time = channel.deliveryTime || channel.performanceTime || channel.estimatedTime || "";
    return '<label class="choice"><input type="radio" name="channel" value="' + escapeHTML(code) +
      '" data-channel-index="' + index + '"><span><strong>' + escapeHTML(name) + '</strong><small>' +
      escapeHTML(code + (time ? " · " + time : "")) + '</small>' +
      (cost != null ? '<span class="price">' + escapeHTML(display(cost) + " " + currency) + '</span>' : "") +
      '</span></label>';
  }).join("") || '<div class="empty-inline">选择发货仓后查询物流渠道</div>';
  renderChannels.current = channels;
  updatePurchaseSummary();
}

function updatePurchaseSummary() {
  const summary = byId("selection-summary");
  const button = byId("purchase-label");
  const ready = Boolean(state.warehouse && state.channel && state.preRequestId);
  button.disabled = !ready;
  summary.classList.toggle("ready", ready);
  if (!ready) {
    summary.textContent = state.warehouse ? "已选择发货仓，请查询并选择物流渠道" : "尚未完成物流选择";
    return;
  }
  summary.textContent = "订单 " + state.orderNo + " · 发货仓 " +
    display(state.warehouse.warehouseName || state.warehouse.warehouseAddressCode) + " · 物流渠道 " +
    display(state.channel.expressShortName || state.channel.expressName || state.channel.expressChannelCode);
}

async function loadChannels(button) {
  if (!state.warehouse) {
    toast("请先选择可用发货仓", true);
    return;
  }
  await busy(button, async function () {
    const data = {
      orderNo: state.orderNo,
      warehouseAddressCode: state.warehouse.warehouseAddressCode || state.warehouse.warehouseCode,
      packageSizeInfo: {
        packageLength: byId("package-length").value.trim(),
        packageWidth: byId("package-width").value.trim(),
        packageHeight: byId("package-height").value.trim(),
        unit: "cm"
      },
      packageWeightInfo: { packageWeight: byId("package-weight").value.trim(), unit: "g" }
    };
    const ids = goodsIDs();
    if (ids.length) data.prePackageInfo = { goodsIds: ids };
    try {
      const payload = await post("shipping/channels", data);
      state.preRequestId = firstValue(infoOf(payload), ["preRequestId"]);
      renderChannels(channelList(payload));
      if (!state.preRequestId) toast("接口未返回 preRequestId，暂时无法在线下单", true);
    } catch (error) {
      renderChannels([]);
      toast(error.message, true);
    }
  });
}

function goodsIDs() {
  return goodsFrom(state.detail).map(function (goods) {
    return goods.goodsId || goods.goods_id || goods.skuId || goods.skuCode;
  }).filter(function (value) { return value !== undefined && value !== null && value !== ""; });
}

async function purchaseLabel(button) {
  if (!state.channel || !state.warehouse || !state.preRequestId) return;
  if (!window.confirm("确认向 SHEIN 提交订单 " + state.orderNo + " 的在线物流下单？")) return;
  await busy(button, async function () {
    const packageInfo = { orderNo: state.orderNo };
    const ids = goodsIDs();
    if (ids.length) packageInfo.goodsIds = ids;
    const data = {
      expressChannelCode: state.channel.expressChannelCode || state.channel.channelCode,
      preRequestId: state.preRequestId,
      packageInfoList: [packageInfo]
    };
    try {
      const payload = await sensitivePost("shipping/place", data, "place-express-order", state.orderNo);
      const info = infoOf(payload);
      if (!firstValue(info, ["placeRequestId", "deliveryNo"])) {
        showResult("在线下单响应", payload);
        throw new Error("在线下单响应缺少履约编号，请检查接口结果");
      }
      await loadTasks();
      byId("fulfillment-dialog").close();
      selectView("labels");
      showResult("订单 " + state.orderNo + " 在线下单", payload);
      toast(payload.cached ? "已返回此前的下单结果" : "在线下单已提交");
    } catch (error) {
      toast(error.message, true);
      state.preRequestId = "";
      updatePurchaseSummary();
    }
  });
}

async function exportAddress(button) {
  const orderNo = byId("address-order-no").value.trim();
  const handleType = Number(byId("address-handle-type").value);
  if (handleType === 2 && !window.confirm("确认导出地址并将订单流转到待发货？")) return;
  await busy(button, async function () {
    try {
      const data = { orderNo: orderNo, handleType: handleType };
      const payload = handleType === 2
        ? await sensitivePost("order/export-address", data, "export-address-transition", orderNo)
        : await post("order/export-address", data);
      showResult("订单 " + orderNo + " 地址结果", payload);
    } catch (error) {
      toast(error.message, true);
    }
  });
}

async function trackLogistics(button) {
  const query = new URLSearchParams();
  const fields = [
    ["orderNo", byId("track-order-no").value.trim()],
    ["packageNo", byId("track-package-no").value.trim()],
    ["waybillNo", byId("track-waybill-no").value.trim()],
    ["returnOrderNo", byId("track-return-order-no").value.trim()]
  ];
  fields.forEach(function (field) { if (field[1]) query.set(field[0], field[1]); });
  await busy(button, async function () {
    try {
      const payload = await request("shipping/track?" + query.toString());
      showResult("物流轨迹", payload);
    } catch (error) {
      toast(error.message, true);
    }
  });
}

async function loadStatus() {
  try {
    const payload = await request("system/shops");
    const data = payload.data || {};
    state.shops = Array.isArray(data.shops) ? data.shops : [];
    const keys = state.shops.map(function (shop) { return shop.code; });
    if (!state.shopKey || !keys.includes(state.shopKey)) {
      state.shopKey = data.default_shop || keys[0] || "beauty-hangers-home";
    }
    sessionStorage.setItem(SHOP_STORAGE_KEY, state.shopKey);
    byId("shop-select").innerHTML = state.shops.map(function (shop) {
      return '<option value="' + escapeHTML(shop.code) + '" ' + (shop.code === state.shopKey ? "selected" : "") +
        '>' + escapeHTML(display(shop.name, shop.code)) + '</option>';
    }).join("") || '<option value="' + escapeHTML(state.shopKey) + '">' + escapeHTML(state.shopKey) + '</option>';
    const selectedShop = state.shops.find(function (shop) { return shop.code === state.shopKey; });
    const selectedShopName = selectedShop ? display(selectedShop.name, state.shopKey) : state.shopKey;
    byId("brand-shop").textContent = selectedShopName;
    byId("crumb-shop").textContent = selectedShopName;
    byId("service-dot").className = "dot";
    byId("service-text").textContent = "Go 服务正常";
    await Promise.all([
      loadOrders(),
      loadManualOrders(),
      loadJobQueue("processing"),
      loadJobQueue("exceptions"),
      loadJobQueue("all"),
      loadBulkBatch(),
      loadTasks()
    ]);
    loadOrders(byId("refresh-orders")).then(loadManualOrders);
  } catch (error) {
    byId("service-dot").className = "dot error";
    byId("service-text").textContent = "服务异常";
    toast(error.message, true);
  }
}

document.querySelectorAll(".nav-button").forEach(function (button) {
  button.addEventListener("click", function () { selectView(button.dataset.view); });
});
byId("menu-button").addEventListener("click", function () {
  byId("sidebar").classList.add("open");
  byId("backdrop").classList.add("visible");
});
byId("backdrop").addEventListener("click", function () {
  byId("sidebar").classList.remove("open");
  byId("backdrop").classList.remove("visible");
});
byId("close-result").addEventListener("click", closeResult);
byId("drawer-backdrop").addEventListener("click", closeResult);
byId("close-fulfillment").addEventListener("click", function () { byId("fulfillment-dialog").close(); });
byId("refresh-warehouses").addEventListener("click", loadFulfillmentContext);
byId("transition-fulfillment-order").addEventListener("click", function (event) {
  transitionFulfillmentOrder(event.currentTarget);
});
byId("refresh-orders").addEventListener("click", async function (event) {
  await loadOrders(event.currentTarget);
  loadManualOrders();
});
byId("run-all-auto").addEventListener("click", function (event) {
  runAutoOrders(state.orders.map(function (order) { return order.order_no; }), event.currentTarget);
});
byId("refresh-processing").addEventListener("click", function (event) { loadJobQueue("processing", event.currentTarget); });
byId("refresh-exceptions").addEventListener("click", function (event) { loadJobQueue("exceptions", event.currentTarget); });
byId("refresh-manual").addEventListener("click", function (event) { loadManualOrders(event.currentTarget); });
byId("refresh-ledger").addEventListener("click", function (event) { loadJobQueue("all", event.currentTarget); });
byId("refresh-tasks").addEventListener("click", function (event) { loadTasks(event.currentTarget); });
byId("order-search").addEventListener("input", function () {
  state.orderPage = 1;
  filterOrders();
});
byId("orders-pager").addEventListener("click", function (event) {
  const button = event.target.closest("[data-order-page]");
  if (!button || button.disabled) return;
  state.orderPage = Number(button.dataset.orderPage);
  filterOrders();
});
byId("manual-search").addEventListener("input", renderManualOrders);
byId("channel-form").addEventListener("submit", function (event) {
  event.preventDefault();
  loadChannels(event.submitter);
});
byId("purchase-label").addEventListener("click", function (event) { purchaseLabel(event.currentTarget); });
byId("address-form").addEventListener("submit", function (event) {
  event.preventDefault();
  exportAddress(event.submitter);
});
byId("tracking-form").addEventListener("submit", function (event) {
  event.preventDefault();
  trackLogistics(event.submitter);
});
byId("shop-select").addEventListener("change", function (event) {
  const selected = event.target.value;
  sessionStorage.setItem(SHOP_STORAGE_KEY, selected);
  const url = new URL(window.location.href);
  url.searchParams.set("shop", selected);
  window.location.assign(url.toString());
});
byId("order-rows").addEventListener("click", function (event) {
  const detailButton = event.target.closest("[data-detail-order]");
  const fulfillmentButton = event.target.closest("[data-fulfill-order]");
  const autoButton = event.target.closest("[data-auto-order]");
  if (detailButton) showOrderDetail(detailButton.dataset.detailOrder);
  if (fulfillmentButton) openFulfillment(fulfillmentButton.dataset.fulfillOrder);
  if (autoButton) runAutoOrders([autoButton.dataset.autoOrder], autoButton);
});
byId("manual-rows").addEventListener("click", function (event) {
  const detailButton = event.target.closest("[data-manual-detail]");
  const fulfillmentButton = event.target.closest("[data-manual-fulfill]");
  if (detailButton) showOrderDetail(detailButton.dataset.manualDetail);
  if (fulfillmentButton) openFulfillment(fulfillmentButton.dataset.manualFulfill);
});
byId("exception-rows").addEventListener("click", function (event) {
  const button = event.target.closest("[data-retry-job]");
  if (!button) return;
  const job = state.exceptionJobs[Number(button.dataset.retryJob)];
  if (job) runAutoOrders([job.order_no], button);
});
byId("task-rows").addEventListener("click", function (event) {
  const checkButton = event.target.closest("[data-check-task]");
  const labelButton = event.target.closest("[data-label-task]");
  if (checkButton) checkTask(Number(checkButton.dataset.checkTask), checkButton);
  if (labelButton) fetchLabel(Number(labelButton.dataset.labelTask), labelButton);
});
byId("warehouse-choices").addEventListener("change", function (event) {
  if (!event.target.matches("[data-warehouse-index]")) return;
  state.warehouse = warehouseList.current ? warehouseList.current[Number(event.target.dataset.warehouseIndex)] : null;
  document.querySelectorAll("#warehouse-choices .choice").forEach(function (choice) {
    choice.classList.toggle("selected", choice.contains(event.target));
  });
  renderChannels([]);
  updatePurchaseSummary();
});
byId("channel-choices").addEventListener("change", function (event) {
  if (!event.target.matches("[data-channel-index]")) return;
  state.channel = renderChannels.current[Number(event.target.dataset.channelIndex)] || null;
  document.querySelectorAll("#channel-choices .choice").forEach(function (choice) {
    choice.classList.toggle("selected", choice.contains(event.target));
  });
  updatePurchaseSummary();
});

warehouseList.current = [];
renderChannels.current = [];

loadStatus();

window.setInterval(function () {
  if (byId("view-processing").classList.contains("active")) {
    loadJobQueue("processing");
  }
  if (byId("view-processing").classList.contains("active") ||
      byId("view-orders").classList.contains("active")) {
    loadBulkBatch();
  }
}, 12000);
