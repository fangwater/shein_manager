"use strict";

const byId = function (id) { return document.getElementById(id); };
const API = "./api/";
const SHOP_STORAGE_KEY = "shein_selected_shop";
const MAX_ACTIONABLE_ORDER_PAGES = 20;
const state = {
  shopKey: new URLSearchParams(window.location.search).get("shop") || sessionStorage.getItem(SHOP_STORAGE_KEY) || "",
  shops: [],
  orders: [],
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

function goodsFrom(order) {
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

function orderNumber(order) {
  return display(firstValue(order, [
    "orderNo", "order_no", "orderSn", "order_sn", "orderId", "order_id",
    "billno", "billNo", "orderCode", "order_code", "parentOrderNo"
  ]), "");
}

function orderStatus(order) {
  return firstValue(order, ["orderStatus", "order_status", "status", "newGoodsStatus"]);
}

function orderTime(order) {
  return firstValue(order, [
    "updateTime", "updatedTime", "orderUpdateTime", "order_updated_at",
    "orderMsgUpdateTime", "orderAllocateTime", "createTime", "createdTime", "orderCreateTime"
  ]);
}

function orderDeadline(order) {
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
  for (const key of ["optionalLogisticsList", "optional_logistics_list"]) {
    if (Array.isArray(order && order[key])) return order[key].map(Number);
  }
  return [];
}

function supportsIntegratedLogistics(order) {
  const options = logisticsOptions(order);
  if (options.length) return options.includes(1);
  return Number(firstValue(order, ["performanceType", "performance_type"])) === 1;
}

function fulfillmentTypeLabel(order) {
  const options = logisticsOptions(order);
  if (options.includes(1) && options.includes(2)) return "集成 / 自发货可选";
  if (!supportsIntegratedLogistics(order)) return "商家自发货";
  const placeType = Number(firstValue(order, ["orderPlaceType", "order_place_type"]));
  if (placeType === 1) return "集成物流 · 平台指定";
  if (placeType === 2) return "集成物流 · 自主选择";
  return "SHEIN 集成物流";
}

function unprocessableReason(order) {
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
  const titles = { orders: "待履约订单", labels: "面单任务", tools: "物流工具" };
  document.querySelectorAll(".nav-button").forEach(function (button) {
    button.classList.toggle("active", button.dataset.view === name);
  });
  document.querySelectorAll(".view").forEach(function (view) {
    view.classList.toggle("active", view.id === "view-" + name);
  });
  byId("crumb-current").textContent = titles[name] || "";
  byId("sidebar").classList.remove("open");
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
  if (status === "1") {
    const reason = unprocessableReason(order);
    return '<button class="table-action primary" data-transition-order="' + escapeHTML(number) + '"' +
      (reason ? ' disabled title="' + escapeHTML(reason) + '"' : "") + ">" +
      (reason ? "暂不可处理" : "转待发货") + "</button>";
  }
  if (status === "2") {
    const integrated = supportsIntegratedLogistics(order);
    return '<button class="table-action primary" data-fulfill-order="' + escapeHTML(number) + '"' +
      (integrated ? "" : ' disabled title="订单仅支持商家自发货"') + ">" +
      (integrated ? "发货" : "仅自发货") + "</button>";
  }
  return '<button class="table-action" disabled>不可操作</button>';
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
    const text = [
      orderNumber(order), firstValue(order, ["salesSite", "site"]), orderStatus(order),
      goodsLines(order).map(function (line) { return line.sku; }).join(" ")
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
    const lines = goodsLines(order);
    const lineHTML = lines.map(function (line) {
      return '<span class="sku-line"><b>' + escapeHTML(line.sku) + '</b><span>× ' + line.quantity + "</span></span>";
    }).join("");
    const status = orderStatus(order);
    const deadline = orderDeadline(order);
    const tone = String(status) === "2" ? "info" : "pending";
    return '<tr><td><div class="order-id"><button class="order-link" data-detail-order="' + escapeHTML(number) + '">' +
      escapeHTML(number) + "</button><small>更新 " + escapeHTML(shortTime(orderTime(order))) +
      '</small></div></td><td><div class="sku-stack">' + (lineHTML || "-") +
      '</div></td><td><div class="order-id"><strong>' + escapeHTML(relativeDeadline(deadline)) +
      "</strong><small>" + escapeHTML(shortTime(deadline)) +
      '</small></div></td><td><div class="order-id"><strong>' + escapeHTML(fulfillmentTypeLabel(order)) +
      "</strong><small>" + escapeHTML(display(firstValue(order, ["salesSite", "site"]), "站点未返回")) +
      '</small></div></td><td><span class="badge ' + tone + '">' + escapeHTML(workflowStatusText(order)) +
      "</span></td><td>" + orderAction(order, number) + "</td></tr>";
  }).join("");
  byId("order-total").textContent = "共 " + filtered.length + " 条";
  byId("metric-orders").textContent = String(filtered.length);
  byId("metric-units").textContent = String(items.reduce(function (total, order) {
    return total + goodsLines(order).reduce(function (sum, line) { return sum + line.quantity; }, 0);
  }, 0));
  byId("metric-actionable").textContent = String(items.filter(orderIsActionable).length);
  byId("nav-order-count").textContent = String(filtered.length);
  renderOrderPager(filtered.length);
}

async function loadOrders(button) {
  await busy(button, async function () {
    const endTime = new Date();
    const startTime = new Date(endTime.getTime() - 47 * 60 * 60 * 1000);
    const data = {
      queryType: 2,
      startTime: formatChinaTime(startTime),
      endTime: formatChinaTime(endTime),
      page: 1,
      pageSize: state.pageSize
    };
    try {
      const results = await Promise.all([loadOrderPages(data, 1), loadOrderPages(data, 2)]);
      const enriched = await loadOrderDetails(results[0].concat(results[1]));
      enriched.orders.sort(function (left, right) {
        const leftDeadline = parseOrderTime(orderDeadline(left));
        const rightDeadline = parseOrderTime(orderDeadline(right));
        if (leftDeadline && rightDeadline && leftDeadline.getTime() !== rightDeadline.getTime()) {
          return leftDeadline.getTime() - rightDeadline.getTime();
        }
        if (leftDeadline) return -1;
        if (rightDeadline) return 1;
        const leftUpdated = parseOrderTime(orderTime(left));
        const rightUpdated = parseOrderTime(orderTime(right));
        return (rightUpdated ? rightUpdated.getTime() : 0) - (leftUpdated ? leftUpdated.getTime() : 0);
      });
      renderOrders(enriched.orders);
      byId("metric-sync").textContent = shortTime(new Date());
      if (enriched.failures) toast(enriched.failures + " 组订单详情暂未加载，请稍后同步", true);
    } catch (error) {
      renderOrders([]);
      toast(error.message, true);
    }
  });
}

async function transitionToShipping(orderNo, button) {
  if (!window.confirm("确认导出订单地址并将订单 " + orderNo + " 流转到待发货？")) return;
  await busy(button, async function () {
    try {
      const detailPayload = await post("order/detail", { orderNoList: [orderNo] });
      const detail = detailFrom(detailPayload) || {};
      const status = String(orderStatus(detail));
      if (status === "2") {
        await loadOrders();
        toast("订单已经处于待发货状态");
        openFulfillment(orderNo);
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
      await loadOrders();
      toast("订单已流转到待发货");
      openFulfillment(orderNo);
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

async function loadFulfillmentContext() {
  byId("warehouse-choices").innerHTML = '<div class="loading-line">正在查询订单与可用仓库</div>';
  try {
    const results = await Promise.all([
      post("order/detail", { orderNoList: [state.orderNo] }),
      post("shipping/warehouses", { orderNo: state.orderNo })
    ]);
    state.detail = detailFrom(results[0]);
    renderOrderSummary(state.detail || {});
    renderWarehouses(warehouseList(results[1]));
    await loadTasks();
  } catch (error) {
    byId("warehouse-choices").innerHTML = '<div class="empty-inline">查询失败，请刷新重试</div>';
    toast(error.message, true);
  }
}

function openFulfillment(orderNo) {
  state.orderNo = orderNo;
  state.detail = null;
  state.warehouse = null;
  state.channel = null;
  state.preRequestId = "";
  byId("fulfillment-title").textContent = "订单 " + orderNo + " 发货";
  renderOrderSummary({});
  renderChannels([]);
  updatePurchaseSummary();
  const dialog = byId("fulfillment-dialog");
  if (!dialog.open) dialog.showModal();
  loadFulfillmentContext();
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
    const keys = state.shops.map(function (shop) { return shop.shop_key; });
    if (!state.shopKey || !keys.includes(state.shopKey)) {
      state.shopKey = data.default_shop_key || keys[0] || data.current_shop_key || "default";
    }
    sessionStorage.setItem(SHOP_STORAGE_KEY, state.shopKey);
    byId("shop-select").innerHTML = state.shops.map(function (shop) {
      return '<option value="' + escapeHTML(shop.shop_key) + '" ' + (shop.shop_key === state.shopKey ? "selected" : "") +
        '>' + escapeHTML(shop.shop_key + (shop.credentials_ready ? "" : "（凭证未就绪）")) + '</option>';
    }).join("") || '<option value="' + escapeHTML(state.shopKey) + '">' + escapeHTML(state.shopKey) + '</option>';
    byId("brand-shop").textContent = state.shopKey;
    byId("crumb-shop").textContent = state.shopKey;
    byId("session-user").textContent = data.user || "已登录";
    byId("service-dot").className = "dot";
    byId("service-text").textContent = "Go 服务正常";
    byId("session-dot").className = "dot";
    loadTasks();
    loadOrders(byId("refresh-orders"));
  } catch (error) {
    byId("service-dot").className = "dot error";
    byId("service-text").textContent = "服务异常";
    byId("session-dot").className = "dot error";
    byId("session-user").textContent = "连接失败";
    toast(error.message, true);
  }
}

document.querySelectorAll(".nav-button").forEach(function (button) {
  button.addEventListener("click", function () { selectView(button.dataset.view); });
});
byId("menu-button").addEventListener("click", function () { byId("sidebar").classList.add("open"); });
byId("backdrop").addEventListener("click", function () { byId("sidebar").classList.remove("open"); });
byId("close-result").addEventListener("click", closeResult);
byId("drawer-backdrop").addEventListener("click", closeResult);
byId("close-fulfillment").addEventListener("click", function () { byId("fulfillment-dialog").close(); });
byId("refresh-warehouses").addEventListener("click", loadFulfillmentContext);
byId("refresh-orders").addEventListener("click", function (event) { loadOrders(event.currentTarget); });
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
  const transitionButton = event.target.closest("[data-transition-order]");
  const fulfillmentButton = event.target.closest("[data-fulfill-order]");
  if (detailButton) showOrderDetail(detailButton.dataset.detailOrder);
  if (transitionButton) transitionToShipping(transitionButton.dataset.transitionOrder, transitionButton);
  if (fulfillmentButton) openFulfillment(fulfillmentButton.dataset.fulfillOrder);
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
