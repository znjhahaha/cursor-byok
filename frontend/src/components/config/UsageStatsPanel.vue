<script setup>
import DailyUsageChart from "@/components/charts/DailyUsageChart.vue";
import HourlyUsageChart from "@/components/charts/HourlyUsageChart.vue";
import Button from "@/components/ui/Button.vue";
import MultiSelect from "@/components/ui/MultiSelect.vue";
import Select from "@/components/ui/Select.vue";
import Skeleton from "@/components/ui/Skeleton.vue";
import { useWindowFocus } from "@/composables/useWindowFocus";
import { appState, syncUsageSeries } from "@/state/appState";
import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";
import { stableUsageModelKey, usageCacheHitRate } from "@/utils/usageStats";
import { computed, onActivated, onMounted, ref, watch } from "vue";

const rangeOptions = [
  { label: "最近 7 天", value: "7" },
  { label: "最近 14 天", value: "14" },
  { label: "最近 30 天", value: "30" },
  { label: "最近 90 天", value: "90" },
];

const metricOptions = [
  { label: "总 Token", value: "totalTokens", icon: "icon-[mdi--counter]" },
  { label: "调用次数", value: "providerCalls", icon: "icon-[mdi--api]" },
  { label: "缓存命中率", value: "cacheHitRate", icon: "icon-[mdi--cached]" },
];

const dimensionOptions = [
  { label: "按 Token 类型", value: "token", icon: "icon-[mdi--layers-outline]" },
  { label: "按中转站", value: "provider", icon: "icon-[mdi--server-network]" },
  { label: "按模型", value: "model", icon: "icon-[mdi--robot-outline]" },
];

const chartTypeOptions = [
  { label: "柱状图", value: "bar", icon: "icon-[mdi--chart-bar]" },
  { label: "折线图", value: "line", icon: "icon-[mdi--chart-line]" },
  { label: "占比图", value: "pie", icon: "icon-[mdi--chart-pie]" },
];

const COUNTER_KEYS = [
  "providerCalls",
  "turnsTotal",
  "inputTokens",
  "outputTokens",
  "cacheReadTokens",
  "cacheWriteTokens",
  "totalTokens",
  "estimatedTokens",
  "estimatedInputTokens",
  "unreportedCalls",
];

const range = ref("30");
const metric = ref("totalTokens");
const dimension = ref("token");
const chartType = ref("bar");
const selectedProviderIDs = ref([]);
const selectedModels = ref([]);
const focusProviderID = ref("");
const expandedDates = ref(new Set());

const days = computed(() => appState.usageSeries.days ?? []);
const providers = computed(() => appState.usageSeries.providers ?? []);
const models = computed(() => appState.usageSeries.models ?? []);
const rawHours = computed(() => appState.usageSeries.hours ?? []);

function asNumber(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function cacheHitRate(item) {
  return usageCacheHitRate(item);
}

function sumCounters(items) {
  const total = Object.fromEntries(COUNTER_KEYS.map((key) => [key, 0]));
  for (const item of items) {
    for (const key of COUNTER_KEYS) {
      total[key] += asNumber(item?.[key]);
    }
  }
  total.cacheHitRate = cacheHitRate(total);
  return total;
}

function decoratePoint(item) {
  return { ...item, cacheHitRate: cacheHitRate(item) };
}

function aggregatePoints(items, idKey, nameKey) {
  const buckets = new Map();
  for (const raw of items) {
    const id = String(raw?.[idKey] ?? "").trim();
    if (!id) {
      continue;
    }
    const current = buckets.get(id) ?? {
      [idKey]: id,
      [nameKey]: String(raw?.[nameKey] || id),
      providerID: String(raw?.providerID || ""),
      providerName: String(raw?.providerName || ""),
      ...Object.fromEntries(COUNTER_KEYS.map((key) => [key, 0])),
    };
    for (const key of COUNTER_KEYS) {
      current[key] += asNumber(raw?.[key]);
    }
    if (raw?.providerID) current.providerID = String(raw.providerID);
    if (raw?.providerName) current.providerName = String(raw.providerName);
    buckets.set(id, current);
  }
  return Array.from(buckets.values()).map(decoratePoint);
}

const providerOptions = computed(() =>
  providers.value
    .map((item) => ({
      label: String(item.providerName || item.providerID || "未知中转站"),
      value: String(item.providerID || ""),
    }))
    .filter((item) => item.value),
);

const modelOptions = computed(() =>
  models.value
    .map((item) => ({
      label: String(item.model || "未知模型"),
      value: stableUsageModelKey(item),
      hint: String(item.channelName || item.providerName || ""),
    }))
    .filter((item) => item.value),
);

// 所有消费方只读取 filteredDays：筛选条件改变后，图、排行、每日明细使用同一口径。
const filteredDays = computed(() => {
  const providerIDs = new Set(selectedProviderIDs.value);
  const modelIDs = new Set(selectedModels.value);
  const hasProviderFilter = providerIDs.size > 0;
  const hasModelFilter = modelIDs.size > 0;

  return days.value.map((rawDay) => {
    const rawProviders = (rawDay.providers ?? []).map(decoratePoint);
    const rawModels = (rawDay.models ?? []).map(decoratePoint);
    const filteredModels = rawModels.filter(
      (item) =>
        (!hasProviderFilter || providerIDs.has(String(item.providerID || ""))) &&
        (!hasModelFilter || modelIDs.has(stableUsageModelKey(item))),
    );

    let filteredProviders = rawProviders.filter(
      (item) => !hasProviderFilter || providerIDs.has(String(item.providerID || "")),
    );
    let totals = decoratePoint(rawDay);

    if (hasModelFilter) {
      totals = sumCounters(filteredModels);
      filteredProviders = aggregatePoints(filteredModels, "providerID", "providerName");
    } else if (hasProviderFilter) {
      totals = sumCounters(filteredProviders);
    }

    return {
      ...rawDay,
      ...totals,
      providers: filteredProviders,
      models: filteredModels,
    };
  });
});

const hours = computed(() => {
  const providerIDs = new Set(selectedProviderIDs.value);
  const modelIDs = new Set(selectedModels.value);
  const hasProviderFilter = providerIDs.size > 0;
  const hasModelFilter = modelIDs.size > 0;
  if (!hasProviderFilter && !hasModelFilter) {
    return rawHours.value;
  }
  return rawHours.value.map((hour) => {
    const scopedModels = (hour.models ?? []).filter(
      (item) =>
        (!hasProviderFilter || providerIDs.has(String(item.providerID || "")))
        && (!hasModelFilter || modelIDs.has(stableUsageModelKey(item))),
    );
    const scopedProviders = (hour.providers ?? []).filter(
      (item) => !hasProviderFilter || providerIDs.has(String(item.providerID || "")),
    );
    const total = hasModelFilter ? sumCounters(scopedModels) : sumCounters(scopedProviders);
    return { ...hour, ...total };
  });
});

const focusProviderName = computed(
  () => providerOptions.value.find((item) => item.value === focusProviderID.value)?.label ?? focusProviderID.value,
);

const chartDays = computed(() => {
  if (!focusProviderID.value || dimension.value !== "model") {
    return filteredDays.value;
  }
  return filteredDays.value.map((day) => {
    const scopedModels = day.models.filter(
      (item) => item.providerID === focusProviderID.value,
    );
    const totals = sumCounters(scopedModels);
    return {
      ...day,
      ...totals,
      providers: day.providers.filter((item) => item.providerID === focusProviderID.value),
      models: scopedModels,
    };
  });
});

const showChartSkeleton = computed(
  () => appState.usageSeriesLoading && days.value.length === 0,
);

const metricLabel = computed(
  () => metricOptions.find((item) => item.value === metric.value)?.label ?? "",
);

const availableDimensions = computed(() => {
  if (metric.value === "cacheHitRate") {
    return [{ label: "整体命中率", value: "total", icon: "icon-[mdi--percent]" }];
  }
  if (metric.value === "providerCalls") {
    return dimensionOptions.filter((item) => item.value !== "token");
  }
  return dimensionOptions;
});

watch(metric, (value, previous) => {
  if (value === "cacheHitRate") {
    dimension.value = "total";
    if (chartType.value === "pie") chartType.value = "line";
    return;
  }
  if (previous === "cacheHitRate" || dimension.value === "total") {
    dimension.value = value === "totalTokens" ? "token" : "provider";
  }
  if (value === "providerCalls" && dimension.value === "token") {
    dimension.value = "provider";
  }
});

function selectChartType(value) {
  if (metric.value === "cacheHitRate" && value === "pie") {
    return;
  }
  chartType.value = value;
}

function formatRate(value) {
  return value == null ? "--" : `${(asNumber(value) * 100).toFixed(1)}%`;
}

function formatValue(value) {
  if (metric.value === "cacheHitRate") return formatRate(value);
  return metric.value === "providerCalls" ? formatInteger(value) : formatCompactInteger(value);
}

function exactValue(value) {
  return metric.value === "cacheHitRate" ? formatRate(value) : formatInteger(value);
}

function modelDetailHint(item) {
  const provider = String(item?.providerName || "").trim();
  const wireModel = String(item?.wireModel || "").trim();
  const displayModel = String(item?.model || "").trim();
  if (provider && wireModel && wireModel !== displayModel) {
    return `${provider} · 上游 ${wireModel}`;
  }
  return provider || (wireModel && wireModel !== displayModel ? `上游 ${wireModel}` : "");
}

const rankingDimension = computed(() => (dimension.value === "model" ? "model" : "provider"));

const rankingConfig = computed(() =>
  rankingDimension.value === "model"
    ? { title: "按模型", idKey: "modelKey", nameKey: "model", empty: "暂无按模型的统计数据" }
    : { title: "按中转站", idKey: "providerID", nameKey: "providerName", empty: "暂无按中转站的统计数据" },
);

const rankedItems = computed(() => {
  const { idKey, nameKey } = rankingConfig.value;
  const raw = filteredDays.value.flatMap((day) =>
    rankingDimension.value === "model" ? day.models : day.providers,
  );
  let source = aggregatePoints(raw, idKey, nameKey);
  if (rankingDimension.value === "model" && focusProviderID.value) {
    source = source.filter((item) => item.providerID === focusProviderID.value);
  }
  const total = source.reduce(
    (sum, item) => sum + (metric.value === "cacheHitRate" ? 0 : asNumber(item[metric.value])),
    0,
  );
  return source
    .map((item) => {
      const value = item[metric.value];
      return {
        id: String(item[idKey] ?? ""),
        name: String(item[nameKey] || item[idKey] || "未知"),
        hint: rankingDimension.value === "model" ? modelDetailHint(item) : "",
        value,
        share:
          metric.value === "cacheHitRate"
            ? asNumber(value)
            : total > 0
              ? asNumber(value) / total
              : 0,
      };
    })
    .filter((item) => item.id)
    .sort((left, right) => asNumber(right.value) - asNumber(left.value));
});

const canDrillDown = computed(() => rankingDimension.value !== "model" && metric.value !== "cacheHitRate");

function handleDrillDown(item) {
  if (!canDrillDown.value) return;
  focusProviderID.value = item.id;
  dimension.value = "model";
}

function clearFocus() {
  focusProviderID.value = "";
  dimension.value = "provider";
}

watch(dimension, (value) => {
  if (value !== "model") focusProviderID.value = "";
});

const dailyRows = computed(() => [...filteredDays.value].reverse());
const summaryTotals = computed(() => sumCounters(filteredDays.value));
const hasFilters = computed(
  () => selectedProviderIDs.value.length > 0 || selectedModels.value.length > 0 || Boolean(focusProviderID.value),
);

function resetFilters() {
  selectedProviderIDs.value = [];
  selectedModels.value = [];
  focusProviderID.value = "";
}

function toggleDate(date) {
  const next = new Set(expandedDates.value);
  if (next.has(date)) next.delete(date);
  else next.add(date);
  expandedDates.value = next;
}

const STATS_STALE_MS = 60_000;
const lastFetchedAt = ref(0);

async function refresh() {
  await syncUsageSeries(Number(range.value));
  lastFetchedAt.value = Date.now();
}

async function refreshIfStale() {
  if (Date.now() - lastFetchedAt.value < STATS_STALE_MS) return;
  await refresh();
}

watch(range, refresh);
onMounted(refresh);
onActivated(refreshIfStale);
useWindowFocus(() => {
  void refreshIfStale();
});
</script>

<template>
  <div class="usage-stats-scroll h-full min-h-0 overflow-y-auto pr-1">
    <div class="flex flex-col gap-3">
      <div class="flex shrink-0 flex-wrap items-center justify-between gap-3">
        <div class="center-row flex-wrap gap-2">
          <Select v-model="range" :options="rangeOptions" aria-label="统计区间" :border="false" />
          <Select v-model="metric" :options="metricOptions" aria-label="统计指标" :border="false" />
          <Select
            v-model="dimension"
            :options="availableDimensions"
            aria-label="展示维度"
            :border="false"
          />
          <MultiSelect
            v-model="selectedProviderIDs"
            :options="providerOptions"
            placeholder="全部中转站"
            aria-label="筛选中转站"
          />
          <MultiSelect
            v-model="selectedModels"
            :options="modelOptions"
            placeholder="全部模型"
            aria-label="筛选模型"
          />
        </div>
        <div class="center-row shrink-0 gap-2">
          <div class="center-row rounded-[7px] border border-[#343434] bg-[#232323] p-0.5">
            <button
              v-for="item in chartTypeOptions"
              :key="item.value"
              type="button"
              class="center-row h-7 w-8 justify-center rounded-[5px] text-[#7f7f7f] transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-30"
              :class="chartType === item.value ? 'bg-[#343434] text-[#86efac]' : 'hover:text-[#d4d4d4]'"
              :title="item.label"
              :disabled="metric === 'cacheHitRate' && item.value === 'pie'"
              @click="selectChartType(item.value)"
            >
              <span :class="[item.icon, 'text-[15px]']"></span>
            </button>
          </div>
          <Button
            variant="default"
            :disabled="appState.usageSeriesLoading"
            :loading="appState.usageSeriesLoading"
            @click="refresh"
          >
            {{ appState.usageSeriesLoading ? "刷新中..." : "刷新" }}
          </Button>
          <Button v-if="hasFilters" variant="default" @click="resetFilters">重置筛选</Button>
        </div>
      </div>

      <div class="grid grid-cols-2 gap-2 xl:grid-cols-4">
        <div class="rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2">
          <div class="text-[11px] text-[#8f8f8f]">调用次数</div>
          <div class="mt-1 font-num text-lg text-[#e5e5e5]">{{ formatInteger(summaryTotals.providerCalls) }}</div>
        </div>
        <div class="rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2">
          <div class="text-[11px] text-[#8f8f8f]">总 Token</div>
          <div class="mt-1 font-num text-lg text-[#e5e5e5]">{{ formatCompactInteger(summaryTotals.totalTokens) }}</div>
        </div>
        <div class="rounded-[8px] border border-[#5a4314] bg-[#2b2413] px-3 py-2">
          <div class="text-[11px] text-[#d4a94f]">估算 Token</div>
          <div class="mt-1 font-num text-lg text-[#fcd34d]">约 {{ formatCompactInteger(summaryTotals.estimatedTokens) }}</div>
        </div>
        <div class="rounded-[8px] border border-[#4b1d1d] bg-[#2a1717] px-3 py-2">
          <div class="text-[11px] text-[#d98f8f]">未报告 usage</div>
          <div class="mt-1 font-num text-lg text-[#fca5a5]">{{ formatInteger(summaryTotals.unreportedCalls) }} 次</div>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-[7px] border border-[#343434] bg-[#202020] px-3 py-2 text-xs text-[#8f8f8f]">
        <span class="text-[#86efac]">时区：北京时间（Asia/Shanghai）</span>
        <span>“约”表示上游未返回 usage 时按实际请求与输出估算。</span>
        <span v-if="appState.usageSeries.legacyUTCDates" class="text-[#f6d77a]">
          升级前的 UTC 日聚合保留原日期，跨日边界可能存在偏差。
        </span>
      </div>

      <div
        v-if="appState.usageSeriesError"
        class="center-row shrink-0 justify-between gap-3 rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]"
      >
        <span class="min-w-0 truncate" :title="appState.usageSeriesError">{{ appState.usageSeriesError }}</span>
        <button type="button" class="shrink-0 text-xs" :disabled="appState.usageSeriesLoading" @click="refresh">
          重试
        </button>
      </div>

      <div
        class="h-[270px] shrink-0 rounded-[8px] border border-[#343434] bg-[#252525] p-3"
        :aria-busy="showChartSkeleton || undefined"
      >
        <Skeleton v-if="showChartSkeleton" variant="chart" />
        <DailyUsageChart
          v-else
          :days="chartDays"
          :metric="metric"
          :dimension="dimension"
          :chart-type="chartType"
        />
      </div>

      <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <div class="rounded-[8px] border border-[#343434] bg-[#252525] p-3">
          <div class="flex items-center justify-between gap-3 pb-2">
            <span class="center-row min-w-0 gap-2 text-sm text-[#d4d4d4]">
              <span class="truncate">{{ rankingConfig.title }} · {{ metricLabel }}</span>
              <button
                v-if="focusProviderID"
                type="button"
                class="center-row max-w-[160px] gap-1 rounded-full border border-[#1ca35a] bg-[#123322] px-2 py-0.5 text-[11px] text-[#86efac]"
                title="返回全部中转站"
                @click="clearFocus"
              >
                <span class="truncate">{{ focusProviderName }}</span>
                <span class="icon-[ic--round-close] text-[12px]"></span>
              </button>
            </span>
            <span class="text-xs text-[#737373]">{{ rankedItems.length }} 项</span>
          </div>
          <div v-if="rankedItems.length === 0" class="py-8 text-center text-sm text-[#a3a3a3]">
            {{ rankingConfig.empty }}
          </div>
          <div v-else class="usage-inner-scroll flex max-h-[205px] flex-col gap-2 overflow-y-auto pr-1">
            <div
              v-for="item in rankedItems"
              :key="item.id"
              class="flex flex-col gap-1 rounded-[6px] px-1 py-0.5 transition-colors"
              :class="canDrillDown ? 'cursor-pointer hover:bg-[#2f2f2f]' : ''"
              :title="canDrillDown ? `查看 ${item.name} 的模型构成` : ''"
              :role="canDrillDown ? 'button' : undefined"
              :tabindex="canDrillDown ? 0 : undefined"
              @click="handleDrillDown(item)"
              @keydown.enter.prevent="handleDrillDown(item)"
            >
              <div class="flex items-center justify-between gap-3 text-sm">
                <span class="center-row min-w-0 gap-2">
                  <span class="truncate text-[#d4d4d4]" :title="item.name">{{ item.name }}</span>
                  <span v-if="item.hint" class="shrink-0 text-[11px] text-[#737373]">{{ item.hint }}</span>
                </span>
                <span class="shrink-0 font-num text-[#e5e5e5]" :title="exactValue(item.value)">
                  {{ formatValue(item.value) }}
                </span>
              </div>
              <div class="h-1.5 overflow-hidden rounded-full bg-[#1f1f1f]">
                <div
                  class="h-full rounded-full bg-[#1ca35a] transition-[width] duration-200"
                  :style="{ width: `${Math.min(100, item.share * 100).toFixed(1)}%` }"
                ></div>
              </div>
            </div>
          </div>
        </div>

        <div class="rounded-[8px] border border-[#343434] bg-[#252525] p-3">
          <div class="flex items-center justify-between pb-2">
            <span class="text-sm text-[#d4d4d4]">按小时分布 · 调用次数</span>
            <span class="text-[11px] text-[#86efac]">北京时间</span>
          </div>
          <HourlyUsageChart :hours="hours" />
        </div>
      </div>

      <div class="rounded-[8px] border border-[#343434] bg-[#252525] p-3">
        <div class="flex items-center justify-between pb-2">
          <span class="text-sm text-[#d4d4d4]">每日明细</span>
          <span class="text-xs text-[#737373]">点击日期展开模型</span>
        </div>
        <div class="usage-inner-scroll max-h-[300px] overflow-auto">
          <table class="w-full min-w-[760px] border-collapse text-xs">
            <thead class="sticky top-0 z-10 bg-[#252525] text-[#8f8f8f]">
              <tr class="border-b border-[#3a3a3a]">
                <th class="px-2 py-2 text-left font-normal">日期</th>
                <th class="px-2 py-2 text-right font-normal">调用</th>
                <th class="px-2 py-2 text-right font-normal">输入</th>
                <th class="px-2 py-2 text-right font-normal">输出</th>
                <th class="px-2 py-2 text-right font-normal">缓存读</th>
                <th class="px-2 py-2 text-right font-normal">缓存写</th>
                <th class="px-2 py-2 text-right font-normal">总 Token</th>
                <th class="px-2 py-2 text-right font-normal">命中率</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="day in dailyRows" :key="day.date">
                <tr
                  class="cursor-pointer border-b border-[#303030] text-[#d4d4d4] transition-colors hover:bg-[#2d2d2d]"
                  @click="toggleDate(day.date)"
                >
                  <td class="px-2 py-2">
                    <span class="center-row gap-1">
                      <span
                        class="icon-[mdi--chevron-right] text-[14px] transition-transform"
                        :class="expandedDates.has(day.date) ? 'rotate-90' : ''"
                      ></span>
                      {{ day.date }}
                    </span>
                  </td>
                  <td class="px-2 py-2 text-right font-num">{{ formatInteger(day.providerCalls) }}</td>
                  <td class="px-2 py-2 text-right font-num">{{ formatCompactInteger(day.inputTokens) }}</td>
                  <td class="px-2 py-2 text-right font-num">{{ formatCompactInteger(day.outputTokens) }}</td>
                  <td class="px-2 py-2 text-right font-num">{{ formatCompactInteger(day.cacheReadTokens) }}</td>
                  <td class="px-2 py-2 text-right font-num">{{ formatCompactInteger(day.cacheWriteTokens) }}</td>
                  <td class="px-2 py-2 text-right font-num">{{ formatCompactInteger(day.totalTokens) }}</td>
                  <td class="px-2 py-2 text-right font-num">{{ formatRate(day.cacheHitRate) }}</td>
                </tr>
                <tr v-if="expandedDates.has(day.date)" class="border-b border-[#343434] bg-[#202020]">
                  <td colspan="8" class="px-7 py-2">
                    <div v-if="day.models.length === 0" class="py-2 text-[#737373]">当日无模型明细</div>
                    <div v-else class="grid grid-cols-1 gap-1.5 xl:grid-cols-2">
                      <div
                        v-for="item in day.models"
                        :key="`${day.date}-${item.modelKey || item.model}-${item.providerID}`"
                        class="flex min-w-0 items-center justify-between gap-3 rounded-[5px] bg-[#282828] px-2 py-1.5"
                      >
                        <span class="min-w-0">
                          <span class="block truncate text-[#cfcfcf]" :title="item.model">{{ item.model }}</span>
                          <span
                            v-if="modelDetailHint(item)"
                            class="mt-0.5 block truncate text-[11px] text-[#737373]"
                            :title="modelDetailHint(item)"
                          >
                            {{ modelDetailHint(item) }}
                          </span>
                        </span>
                        <span class="center-row shrink-0 gap-3 text-[#8f8f8f]">
                          <span>{{ formatInteger(item.providerCalls) }} 次</span>
                          <span class="font-num text-[#d4d4d4]">
                            {{ item.estimatedTokens > 0 ? "约 " : "" }}{{ formatCompactInteger(item.totalTokens) }}
                          </span>
                        </span>
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.usage-stats-scroll,
.usage-inner-scroll {
  scrollbar-width: thin;
  scrollbar-color: #4a4a4a transparent;
}

.usage-stats-scroll::-webkit-scrollbar,
.usage-inner-scroll::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.usage-stats-scroll::-webkit-scrollbar-thumb,
.usage-inner-scroll::-webkit-scrollbar-thumb {
  border-radius: 999px;
  background: #4a4a4a;
}
</style>
