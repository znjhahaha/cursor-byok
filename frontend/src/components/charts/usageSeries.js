import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";

export const TOKEN_LAYERS = [
  { key: "inputTokens", label: "输入（非缓存）", color: "#2f6b4f" },
  { key: "cacheReadTokens", label: "缓存输入", color: "#215b6b" },
  { key: "cacheWriteTokens", label: "缓存写入", color: "#e0a03a" },
  { key: "outputTokens", label: "模型输出", color: "#5f5d94" },
];

export const USAGE_PALETTE = [
  "#1ca35a",
  "#3b82f6",
  "#e0a03a",
  "#a855f7",
  "#ef4444",
  "#14b8a6",
  "#ec4899",
  "#84cc16",
];

export const METRIC_LABELS = {
  totalTokens: "总请求",
  providerCalls: "调用次数",
};

export function asUsageNumber(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function formatUsageValue(value, metric) {
  return metric === "providerCalls" ? formatInteger(value) : formatCompactInteger(value);
}

function buildTokenSeries(days) {
  return TOKEN_LAYERS.map((layer) => ({
    id: layer.key,
    name: layer.label,
    color: layer.color,
    values: days.map((day) => asUsageNumber(day?.[layer.key])),
  }));
}

function buildGroupedSeries(days, listKey, idKey, nameKey, metric) {
  const order = [];
  const nameByID = new Map();
  const valuesByID = new Map();

  for (const day of days) {
    const list = Array.isArray(day?.[listKey]) ? day[listKey] : [];
    for (const item of list) {
      const id = String(item?.[idKey] ?? "").trim();
      if (!id) {
        continue;
      }
      if (!valuesByID.has(id)) {
        order.push(id);
        valuesByID.set(id, new Map());
        nameByID.set(id, String(item?.[nameKey] || id).trim());
      }
      valuesByID.get(id).set(day.date, asUsageNumber(item?.[metric]));
    }
  }

  return order.map((id, index) => ({
    id,
    name: nameByID.get(id) || id,
    color: USAGE_PALETTE[index % USAGE_PALETTE.length],
    values: days.map((day) => valuesByID.get(id)?.get(day.date) ?? 0),
  }));
}

function buildTotalSeries(days, metric) {
  return [
    {
      id: "total",
      name: METRIC_LABELS[metric] ?? "合计",
      color: USAGE_PALETTE[0],
      values: days.map((day) => asUsageNumber(day?.[metric])),
    },
  ];
}

// 三种维度统一产出 { id, name, color, values }[]，渲染层无需理解原始数据结构。
export function buildUsageSeries(days, { dimension = "token", metric = "totalTokens" } = {}) {
  const normalizedDays = Array.isArray(days) ? days : [];
  if (dimension === "model") {
    const grouped = buildGroupedSeries(normalizedDays, "models", "model", "model", metric);
    return grouped.length > 0 ? grouped : buildTotalSeries(normalizedDays, metric);
  }
  if (dimension === "provider") {
    const grouped = buildGroupedSeries(
      normalizedDays,
      "providers",
      "providerID",
      "providerName",
      metric,
    );
    return grouped.length > 0 ? grouped : buildTotalSeries(normalizedDays, metric);
  }
  return metric === "totalTokens"
    ? buildTokenSeries(normalizedDays)
    : buildTotalSeries(normalizedDays, metric);
}

export function buildShareSeries(days, options = {}) {
  return buildUsageSeries(days, options)
    .map((item) => ({
      ...item,
      value: item.values.reduce((sum, value) => sum + asUsageNumber(value), 0),
    }))
    .filter((item) => item.value > 0);
}