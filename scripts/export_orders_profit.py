from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

import nbformat as nbf
import pandas as pd
import psycopg

from shein_api_manager.config import load_settings


BASE_DIR = Path(__file__).resolve().parents[1]
EXPORT_DIR = BASE_DIR / "exports"
NOTEBOOK_DIR = BASE_DIR / "notebooks"
ORDER_ITEMS_PARQUET = EXPORT_DIR / "shein_order_items_profit.parquet"
ORDERS_PARQUET = EXPORT_DIR / "shein_orders_profit_summary.parquet"
NOTEBOOK_PATH = NOTEBOOK_DIR / "shein_order_profit_analysis.ipynb"

# User supplied cost rules, USD per SKU package.
REVENUE_EXCLUDED_ORDER_STATUSES = {"1", "2", "6"}
AFTER_SALES_COST_ORDER_STATUSES = {"8", "9"}
PENDING_PICKUP_ORDER_STATUS = "7"

COST_RULES = {
    10: {"product_cost": 1.87, "packaging_fee": 0.90, "composition": "10"},
    20: {"product_cost": 2.69, "packaging_fee": 0.90, "composition": "20"},
    30: {"product_cost": 1.87 + 2.69, "packaging_fee": 0.90 + 0.50, "composition": "10+20"},
    50: {"product_cost": 6.21, "packaging_fee": 0.90, "composition": "50"},
    100: {"product_cost": 6.21 * 2, "packaging_fee": 0.90 + 0.50, "composition": "50*2"},
}


def money(value: Any, default: float = 0.0) -> float:
    if value in (None, ""):
        return default
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def find_us_attr(goods: dict[str, Any]) -> str:
    attrs = goods.get("skuAttribute") or []
    if isinstance(attrs, list):
        for attr in attrs:
            if isinstance(attr, dict) and attr.get("language") == "US":
                name = attr.get("attrName")
                if name and name != "-":
                    return str(name)
        for attr in attrs:
            if isinstance(attr, dict):
                name = attr.get("attrName")
                if name and name != "-":
                    return str(name)
    return ""


def _pcs_matches(value: str) -> list[int]:
    pattern = r"(100|50|30|20|10)\s*[- ]?\s*(?:pcs?|pieces?|packs?)\b"
    return [int(match) for match in re.findall(pattern, value or "", flags=re.IGNORECASE)]


def infer_pcs(goods: dict[str, Any]) -> int | None:
    attr = find_us_attr(goods)
    # SKU attribute is the variant actually sold. Ignore add-ons after plus signs,
    # e.g. "100Pack + 20Pcs Clasps" should be 100pcs, not 20pcs.
    attr_main = re.split(r"\+|,|;", attr or "", maxsplit=1)[0]
    for source in (attr_main, attr, str(goods.get("sellerSku") or "")):
        matches = _pcs_matches(source)
        if matches:
            return matches[0]

    # Product titles often list all variants, such as "100/50/30pcs". Use the
    # title only when it points to a single known package size.
    title_matches = sorted(set(_pcs_matches(str(goods.get("goodsTitle") or ""))))
    if len(title_matches) == 1:
        return title_matches[0]
    return None


def product_cost_for_pcs(pcs: int | None) -> tuple[float, float, str, bool]:
    if pcs in COST_RULES:
        rule = COST_RULES[pcs]
        return (
            float(rule["product_cost"]),
            float(rule["packaging_fee"]),
            str(rule["composition"]),
            True,
        )
    return 0.0, 0.0, "unknown", False


def fetch_orders() -> list[
    tuple[str, str | None, str | None, str, str | None, str | None, str | None, str | None, bool, str, str | None, dict[str, Any]]
]:
    settings = load_settings()
    if not settings.database_url:
        raise RuntimeError("DATABASE_URL is required")
    with psycopg.connect(settings.database_url) as conn:
        rows = conn.execute(
            """
            SELECT
                o.order_no,
                o.order_status,
                o.order_status_label,
                o.order_status_normalized,
                o.order_type,
                o.order_type_label,
                o.order_created_at,
                o.order_updated_at,
                COALESCE(r.return_count, 0) > 0 AS return_detected,
                r.return_status,
                r.return_created_at AS order_return_time,
                o.detail_payload
            FROM shein_orders o
            LEFT JOIN (
                SELECT
                    shop_key,
                    order_no,
                    count(*) AS return_count,
                    string_agg(DISTINCT return_status_label, ',' ORDER BY return_status_label) AS return_status,
                    min(request_return_time) AS return_created_at
                FROM shein_order_returns
                WHERE order_no IS NOT NULL
                GROUP BY shop_key, order_no
            ) r ON r.shop_key = o.shop_key AND r.order_no = o.order_no
            WHERE o.detail_payload IS NOT NULL
            ORDER BY o.order_created_at NULLS LAST, o.order_no
            """
        ).fetchall()
    return rows


def build_dataframes() -> tuple[pd.DataFrame, pd.DataFrame]:
    rows = fetch_orders()
    item_rows: list[dict[str, Any]] = []
    for (
        order_no,
        order_status,
        order_status_label,
        order_status_normalized,
        order_type,
        order_type_label,
        order_created_at,
        order_updated_at,
        return_detected,
        return_status,
        order_return_time,
        detail,
    ) in rows:
        if not isinstance(detail, dict):
            continue
        receive = detail.get("receiveMsg") or {}
        goods_list = detail.get("orderGoodsInfoList") or []
        if not isinstance(goods_list, list):
            goods_list = []
        product_total_price = money(detail.get("productTotalPrice"))
        estimated_gross_income = money(detail.get("estimatedGrossIncome"))
        seller_shipping_fee = money(detail.get("sellerShippingFee"))
        total_performance_service_charge = money(detail.get("totalPerformanceServiceCharge"))
        total_service_charge = money(detail.get("totalServiceCharge"))
        total_cost_price = money(detail.get("totalCostPrice"))
        total_sale_tax = money(detail.get("totalSaleTax"))
        total_commission = money(detail.get("totalCommission"))

        prepared_goods: list[dict[str, Any]] = []
        for goods in goods_list:
            if not isinstance(goods, dict):
                continue
            item_estimated_income = money(goods.get("estimatedIncome"))
            base_item_revenue = item_estimated_income
            goods_weight = money(goods.get("goodsWeight"))
            prepared_goods.append(
                {
                    "goods": goods,
                    "base_item_revenue": base_item_revenue,
                    "goods_weight": goods_weight,
                }
            )

        total_weight = sum(item["goods_weight"] for item in prepared_goods)
        total_base_revenue = sum(item["base_item_revenue"] for item in prepared_goods)
        for item in prepared_goods:
            goods = item["goods"]
            base_item_revenue = item["base_item_revenue"]
            goods_weight = item["goods_weight"]
            if total_weight > 0:
                allocation_ratio = goods_weight / total_weight
                allocation_method = "weight"
            elif total_base_revenue > 0:
                allocation_ratio = base_item_revenue / total_base_revenue
                allocation_method = "income"
            else:
                allocation_ratio = 1 / len(prepared_goods) if prepared_goods else 0.0
                allocation_method = "equal"

            pcs = infer_pcs(goods)
            product_cost, packaging_fee, composition, cost_rule_matched = product_cost_for_pcs(pcs)
            item_estimated_income = money(goods.get("estimatedIncome"))
            item_shein_cost_price = money(goods.get("costPrice"))
            item_performance_service_charge = money(goods.get("performanceServiceCharge"))
            internal_cost = product_cost + packaging_fee
            original_base_revenue_allocated = base_item_revenue
            original_shipping_fee_allocated = seller_shipping_fee * allocation_ratio
            original_performance_service_charge_allocated = total_performance_service_charge * allocation_ratio
            original_gross_revenue_allocated = original_base_revenue_allocated + original_shipping_fee_allocated
            original_profit = original_gross_revenue_allocated - original_performance_service_charge_allocated - internal_cost
            order_status_code = str(order_status or "")
            is_return_order = bool(return_detected)
            is_revenue_excluded_status = order_status_code in REVENUE_EXCLUDED_ORDER_STATUSES
            is_after_sales_status = order_status_code in AFTER_SALES_COST_ORDER_STATUSES
            is_pending_pickup_without_fulfillment_fee = (
                order_status_code == PENDING_PICKUP_ORDER_STATUS
                and total_performance_service_charge <= 0
            )
            pnl_product_cost = product_cost
            pnl_packaging_fee = packaging_fee
            after_sales_cost = original_gross_revenue_allocated if is_after_sales_status else 0.0
            if is_return_order:
                pnl_policy = "return_profit_zeroed"
                base_revenue_allocated = 0.0
                shipping_fee_allocated = 0.0
                performance_service_charge_allocated = 0.0
                gross_revenue_allocated = 0.0
                after_sales_cost = 0.0
                profit = 0.0
            elif is_revenue_excluded_status or is_pending_pickup_without_fulfillment_fee:
                pnl_policy = (
                    "pending_pickup_without_fulfillment_fee"
                    if is_pending_pickup_without_fulfillment_fee
                    else "revenue_excluded_status"
                )
                base_revenue_allocated = 0.0
                shipping_fee_allocated = 0.0
                performance_service_charge_allocated = 0.0
                gross_revenue_allocated = 0.0
                pnl_product_cost = 0.0
                pnl_packaging_fee = 0.0
                after_sales_cost = 0.0
                profit = 0.0
            elif is_after_sales_status:
                pnl_policy = "after_sales_cost"
                base_revenue_allocated = original_base_revenue_allocated
                shipping_fee_allocated = original_shipping_fee_allocated
                performance_service_charge_allocated = original_performance_service_charge_allocated
                gross_revenue_allocated = original_gross_revenue_allocated
                profit = gross_revenue_allocated - performance_service_charge_allocated - pnl_product_cost - pnl_packaging_fee - after_sales_cost
            else:
                pnl_policy = "standard"
                base_revenue_allocated = original_base_revenue_allocated
                shipping_fee_allocated = original_shipping_fee_allocated
                performance_service_charge_allocated = original_performance_service_charge_allocated
                gross_revenue_allocated = original_gross_revenue_allocated
                profit = original_profit
            pnl_internal_cost = pnl_product_cost + pnl_packaging_fee
            item_rows.append(
                {
                    "order_no": order_no,
                    "order_status": order_status,
                    "order_status_label": order_status_label,
                    "order_status_normalized": order_status_normalized,
                    "order_type": order_type,
                    "order_type_label": order_type_label,
                    "order_created_at": order_created_at,
                    "order_updated_at": order_updated_at,
                    "return_detected": is_return_order,
                    "return_status": return_status,
                    "order_return_time": order_return_time,
                    "pnl_policy": pnl_policy,
                    "revenue_excluded_status": is_revenue_excluded_status,
                    "pending_pickup_without_fulfillment_fee": is_pending_pickup_without_fulfillment_fee,
                    "after_sales_status": is_after_sales_status,
                    "sales_site": detail.get("salesSite"),
                    "order_currency": detail.get("orderCurrency"),
                    "sale_currency": detail.get("saleCurrency"),
                    "country": receive.get("country") if isinstance(receive, dict) else None,
                    "province": receive.get("province") if isinstance(receive, dict) else None,
                    "city": receive.get("city") if isinstance(receive, dict) else None,
                    "post_code": receive.get("postCode") if isinstance(receive, dict) else None,
                    "goods_id": str(goods.get("goodsId") or ""),
                    "sku_code": goods.get("skuCode"),
                    "skc": goods.get("skc"),
                    "goods_sn": goods.get("goodsSn"),
                    "seller_sku": goods.get("sellerSku"),
                    "sku_attr_us": find_us_attr(goods),
                    "goods_title": goods.get("goodsTitle"),
                    "pcs": pcs,
                    "goods_weight": goods_weight,
                    "allocation_method": allocation_method,
                    "allocation_ratio": allocation_ratio,
                    "cost_composition": composition,
                    "cost_rule_matched": cost_rule_matched,
                    "product_cost_rule_usd": product_cost,
                    "packaging_fee_rule_usd": packaging_fee,
                    "internal_cost_usd": internal_cost,
                    "pnl_product_cost_usd": pnl_product_cost,
                    "pnl_packaging_fee_usd": pnl_packaging_fee,
                    "pnl_internal_cost_usd": pnl_internal_cost,
                    "after_sales_cost_usd": after_sales_cost,
                    "item_estimated_income": item_estimated_income,
                    "item_shein_cost_price": item_shein_cost_price,
                    "item_performance_service_charge": item_performance_service_charge,
                    "base_revenue_allocated_usd": base_revenue_allocated,
                    "shipping_fee_allocated_usd": shipping_fee_allocated,
                    "performance_service_charge_allocated_usd": performance_service_charge_allocated,
                    "gross_revenue_allocated_usd": gross_revenue_allocated,
                    "profit_usd": profit,
                    "profit_margin": profit / gross_revenue_allocated if gross_revenue_allocated else None,
                    "original_base_revenue_allocated_usd": original_base_revenue_allocated,
                    "original_shipping_fee_allocated_usd": original_shipping_fee_allocated,
                    "original_performance_service_charge_allocated_usd": original_performance_service_charge_allocated,
                    "original_gross_revenue_allocated_usd": original_gross_revenue_allocated,
                    "original_profit_usd": original_profit,
                    "order_product_total_price": product_total_price,
                    "order_estimated_gross_income": estimated_gross_income,
                    "order_seller_shipping_fee": seller_shipping_fee,
                    "order_total_performance_service_charge": total_performance_service_charge,
                    "order_total_service_charge": total_service_charge,
                    "order_total_cost_price": total_cost_price,
                    "order_total_sale_tax": total_sale_tax,
                    "order_total_commission": total_commission,
                    "raw_detail_json": json.dumps(detail, ensure_ascii=False, sort_keys=True),
                }
            )

    items = pd.DataFrame(item_rows)
    if items.empty:
        return items, pd.DataFrame()

    numeric_cols = [
        "goods_weight",
        "allocation_ratio",
        "product_cost_rule_usd",
        "packaging_fee_rule_usd",
        "internal_cost_usd",
        "pnl_product_cost_usd",
        "pnl_packaging_fee_usd",
        "pnl_internal_cost_usd",
        "after_sales_cost_usd",
        "item_estimated_income",
        "item_shein_cost_price",
        "item_performance_service_charge",
        "base_revenue_allocated_usd",
        "shipping_fee_allocated_usd",
        "performance_service_charge_allocated_usd",
        "gross_revenue_allocated_usd",
        "profit_usd",
        "original_base_revenue_allocated_usd",
        "original_shipping_fee_allocated_usd",
        "original_performance_service_charge_allocated_usd",
        "original_gross_revenue_allocated_usd",
        "original_profit_usd",
    ]
    for col in numeric_cols:
        items[col] = pd.to_numeric(items[col], errors="coerce").fillna(0.0)

    orders = (
        items.groupby("order_no", dropna=False)
        .agg(
            order_created_at=("order_created_at", "first"),
            order_status=("order_status", "first"),
            order_status_label=("order_status_label", "first"),
            order_status_normalized=("order_status_normalized", "first"),
            order_type=("order_type", "first"),
            order_type_label=("order_type_label", "first"),
            return_detected=("return_detected", "max"),
            return_status=("return_status", "first"),
            order_return_time=("order_return_time", "first"),
            pnl_policy=("pnl_policy", "first"),
            revenue_excluded_status=("revenue_excluded_status", "max"),
            pending_pickup_without_fulfillment_fee=("pending_pickup_without_fulfillment_fee", "max"),
            after_sales_status=("after_sales_status", "max"),
            sales_site=("sales_site", "first"),
            country=("country", "first"),
            province=("province", "first"),
            city=("city", "first"),
            goods_lines=("goods_id", "count"),
            total_pcs=("pcs", "sum"),
            total_weight=("goods_weight", "sum"),
            matched_lines=("cost_rule_matched", "sum"),
            base_revenue_usd=("base_revenue_allocated_usd", "sum"),
            shipping_fee_revenue_usd=("shipping_fee_allocated_usd", "sum"),
            performance_service_charge_usd=("performance_service_charge_allocated_usd", "sum"),
            gross_revenue_usd=("gross_revenue_allocated_usd", "sum"),
            product_cost_usd=("product_cost_rule_usd", "sum"),
            packaging_fee_usd=("packaging_fee_rule_usd", "sum"),
            internal_cost_usd=("internal_cost_usd", "sum"),
            pnl_product_cost_usd=("pnl_product_cost_usd", "sum"),
            pnl_packaging_fee_usd=("pnl_packaging_fee_usd", "sum"),
            pnl_internal_cost_usd=("pnl_internal_cost_usd", "sum"),
            after_sales_cost_usd=("after_sales_cost_usd", "sum"),
            profit_usd=("profit_usd", "sum"),
            original_base_revenue_usd=("original_base_revenue_allocated_usd", "sum"),
            original_shipping_fee_revenue_usd=("original_shipping_fee_allocated_usd", "sum"),
            original_performance_service_charge_usd=("original_performance_service_charge_allocated_usd", "sum"),
            original_gross_revenue_usd=("original_gross_revenue_allocated_usd", "sum"),
            original_profit_usd=("original_profit_usd", "sum"),
            order_estimated_gross_income=("order_estimated_gross_income", "first"),
            order_total_performance_service_charge=("order_total_performance_service_charge", "first"),
            order_seller_shipping_fee=("order_seller_shipping_fee", "first"),
        )
        .reset_index()
    )
    orders["profit_margin"] = orders["profit_usd"] / orders["gross_revenue_usd"].replace({0: pd.NA})
    return items, orders

def write_exports() -> tuple[pd.DataFrame, pd.DataFrame]:
    EXPORT_DIR.mkdir(parents=True, exist_ok=True)
    NOTEBOOK_DIR.mkdir(parents=True, exist_ok=True)
    items, orders = build_dataframes()
    items.to_parquet(ORDER_ITEMS_PARQUET, index=False)
    orders.to_parquet(ORDERS_PARQUET, index=False)
    return items, orders


def create_notebook() -> None:
    nb = nbf.v4.new_notebook()
    nb.cells = [
        nbf.v4.new_markdown_cell(
            """# SHEIN 订单利润分析\n\n本 Notebook 基于已导出的 Parquet 数据分析最近同步订单利润。成本规则来自人工输入：\n\n- 10pcs：货品成本 1.87，打包费 0.90\n- 20pcs：货品成本 2.69，打包费 0.90\n- 50pcs：货品成本 6.21，打包费 0.90\n- 30pcs = 10 + 20，打包费 0.90 + 0.50 续件\n- 100pcs = 50 * 2，打包费 0.90 + 0.50 续件\n\n利润口径：非退货订单 `利润 = goods_estimatedIncome + sellerShippingFee按重量分摊 - totalPerformanceServiceCharge按重量分摊 - 货品成本 - 打包费`。订单级运费收入和履约物流费优先按商品 `goodsWeight` 分摊；缺重量时按收入占比分摊，再缺则平均分摊。订单状态 1/2/6 的收入和利润从 PnL 剔除；订单状态 7 仅在 `totalPerformanceServiceCharge > 0` 时计入，否则从 PnL 剔除；订单状态 8/9 的原始收入计入售后成本 `after_sales_cost_usd`；退货订单仍计入货品成本和打包费，但有效收入、有效运费、有效履约费和利润均按 0 进入 PnL；原始未调整金额保留在 `original_*` 字段。"""
        ),
        nbf.v4.new_code_cell(
            """from pathlib import Path\nimport pandas as pd\n\nBASE_DIR = Path('/home/ubuntu/shein-api-manager')\nitems = pd.read_parquet(BASE_DIR / 'exports/shein_order_items_profit.parquet')\norders = pd.read_parquet(BASE_DIR / 'exports/shein_orders_profit_summary.parquet')\n\nitems.shape, orders.shape"""
        ),
        nbf.v4.new_code_cell(
            """summary = pd.DataFrame([{\n    '订单数': orders['order_no'].nunique(),\n    '商品行数': len(items),\n    '总收入_美元': items['gross_revenue_allocated_usd'].sum(),\n    '货品成本_美元': items['product_cost_rule_usd'].sum(),\n    '打包费_美元': items['packaging_fee_rule_usd'].sum(),\n    '内部成本_美元': items['internal_cost_usd'].sum(),\n    '利润_美元': items['profit_usd'].sum(),\n    '利润率': items['profit_usd'].sum() / items['gross_revenue_allocated_usd'].sum(),\n    '成本规则未匹配行数': (~items['cost_rule_matched']).sum(),\n}])\nsummary"""
        ),
        nbf.v4.new_code_cell(
            """by_pcs = (items.groupby('pcs', dropna=False)\n    .agg(商品行数=('goods_id','count'),\n         订单数=('order_no','nunique'),\n         收入=('gross_revenue_allocated_usd','sum'),\n         货品成本=('product_cost_rule_usd','sum'),\n         打包费=('packaging_fee_rule_usd','sum'),\n         利润=('profit_usd','sum'))\n    .reset_index())\nby_pcs['利润率'] = by_pcs['利润'] / by_pcs['收入']\nby_pcs.sort_values('利润', ascending=False)"""
        ),
        nbf.v4.new_code_cell(
            """by_attr = (items.groupby(['pcs','sku_attr_us'], dropna=False)\n    .agg(商品行数=('goods_id','count'),\n         订单数=('order_no','nunique'),\n         收入=('gross_revenue_allocated_usd','sum'),\n         内部成本=('internal_cost_usd','sum'),\n         利润=('profit_usd','sum'))\n    .reset_index())\nby_attr['利润率'] = by_attr['利润'] / by_attr['收入']\nby_attr.sort_values('利润', ascending=False).head(30)"""
        ),
        nbf.v4.new_code_cell(
            """by_state = (items.groupby(['country','province'], dropna=False)\n    .agg(订单数=('order_no','nunique'),\n         商品行数=('goods_id','count'),\n         收入=('gross_revenue_allocated_usd','sum'),\n         利润=('profit_usd','sum'))\n    .reset_index())\nby_state['利润率'] = by_state['利润'] / by_state['收入']\nby_state.sort_values('利润', ascending=False).head(30)"""
        ),
        nbf.v4.new_code_cell(
            """unmatched = items.loc[~items['cost_rule_matched'], [\n    'order_no','sku_attr_us','goods_title','seller_sku','gross_revenue_allocated_usd'\n]]\nunmatched.head(50)"""
        ),
        nbf.v4.new_code_cell(
            """top_loss = items.sort_values('profit_usd').loc[:, [\n    'order_no','order_created_at','sku_attr_us','pcs','gross_revenue_allocated_usd',\n    'product_cost_rule_usd','packaging_fee_rule_usd','profit_usd','profit_margin'\n]].head(30)\ntop_loss"""
        ),
    ]
    nbf.write(nb, NOTEBOOK_PATH)


def main() -> None:
    items, orders = write_exports()
    create_notebook()
    print(json.dumps({
        "order_items_parquet": str(ORDER_ITEMS_PARQUET),
        "orders_parquet": str(ORDERS_PARQUET),
        "notebook": str(NOTEBOOK_PATH),
        "item_rows": int(len(items)),
        "order_rows": int(len(orders)),
        "orders": int(orders["order_no"].nunique()) if not orders.empty else 0,
        "total_revenue_usd": round(float(items["gross_revenue_allocated_usd"].sum()), 4) if not items.empty else 0,
        "total_internal_cost_usd": round(float(items["pnl_internal_cost_usd"].sum()), 4) if not items.empty else 0,
        "total_after_sales_cost_usd": round(float(items["after_sales_cost_usd"].sum()), 4) if not items.empty else 0,
        "total_profit_usd": round(float(items["profit_usd"].sum()), 4) if not items.empty else 0,
        "unmatched_rows": int((~items["cost_rule_matched"]).sum()) if not items.empty else 0,
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
