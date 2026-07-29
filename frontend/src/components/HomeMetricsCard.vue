<script setup>
import CacheHitRateChart from "@/components/charts/CacheHitRateChart.vue";
import DailyUsageChart from "@/components/charts/DailyUsageChart.vue";
import { buildUsageSeries } from "@/components/charts/usageSeries";
import TrendRangePicker from "@/components/TrendRangePicker.vue";
import Switch from "@/components/ui/Switch.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { useWindowGrow } from "@/composables/useWindowGrow";
import { appState, saveIncludeCacheWriteInHitRate, syncUsageSeries } from "@/state/appState";
import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";
import { computed, ref, watch } from "vue";

const emit = defineEmits([
  "refresh",
  "open-ad",
  "open-provider-config",
  "open-model-config",
  "open-usage-stats",
]);

const TOKEN_PRICE_PER_MILLION = {
  input: 5,
  output: 25,
  cacheRead: 0.5,
  cacheWrite: 6.25,
};

const props = defineProps({
  metrics: {
    type: Object,
    required: true,
  },
  loading: {
    type: Boolean,
    default: false,
  },
  error: {
    type: String,
    default: "",
  },
  homeAd: {
    type: Object,
    default: null,
  },
  homeAds: {
    type: Array,
    default: () => [],
  },
});

const homeMetricsConfigSaving = ref(false);
const homeMetricsConfigError = ref("");

function normalizeNumber(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return 0;
  }
  return Math.round(number);
}

function formatMetricValue(value) {
  const full = formatInteger(value);
  const compact = formatCompactInteger(value);
  return full === compact ? full : `${full} (${compact})`;
}

function formatRateLabel(value) {
  const rate = Number(value);
  if (!Number.isFinite(rate)) {
    return "暂无数据";
  }
  return `${(Math.max(0, Math.min(1, rate)) * 100).toFixed(2)}%`;
}

function calculateRate(numerator, denominator) {
  const top = normalizeNumber(numerator);
  const bottom = normalizeNumber(denominator);
  if (bottom <= 0) {
    return null;
  }
  return top / bottom;
}

function priceTokens(tokens, pricePerMillion) {
  return (normalizeNumber(tokens) / 1_000_000) * pricePerMillion;
}

function formatUSD(value) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) {
    return "$0.00";
  }
  if (amount > 0 && amount < 0.01) {
    return "<$0.01";
  }
  return `$${amount.toFixed(2)}`;
}

const cacheReadTokensTotal = computed(() => normalizeNumber(props.metrics?.cacheReadTokens));
const cacheWriteTokensTotal = computed(() => normalizeNumber(props.metrics?.cacheWriteTokens));

const inputTokensTotal = computed(() => {
  const promptTokensTotal = normalizeNumber(props.metrics?.promptTokensTotal);
  return Math.max(0, promptTokensTotal - cacheReadTokensTotal.value - cacheWriteTokensTotal.value);
});

const defaultCacheHitRate = computed(() =>
  calculateRate(cacheReadTokensTotal.value, cacheReadTokensTotal.value + inputTokensTotal.value),
);

const cacheReuseRate = computed(() =>
  calculateRate(
    cacheReadTokensTotal.value,
    cacheReadTokensTotal.value + cacheWriteTokensTotal.value + inputTokensTotal.value,
  ),
);

const includeCacheWriteInHitRate = computed(() => appState.includeCacheWriteInHitRate);

const selectedCacheHitRate = computed(() =>
  includeCacheWriteInHitRate.value ? cacheReuseRate.value : defaultCacheHitRate.value,
);

const selectedCacheRateModeLabel = computed(() =>
  includeCacheWriteInHitRate.value ? "计入缓存创建" : "默认口径",
);

const validTurnsRate = computed(() => {
  const turnsTotal = normalizeNumber(props.metrics?.turnsTotal);
  if (turnsTotal <= 0) {
    return null;
  }
  return normalizeNumber(props.metrics?.validTurnsTotal) / turnsTotal;
});

const completionTokensTotal = computed(() => {
  const requestTokensTotal = normalizeNumber(props.metrics?.requestTokensTotal);
  const promptTokensTotal = normalizeNumber(props.metrics?.promptTokensTotal);
  return Math.max(0, requestTokensTotal - promptTokensTotal);
});

const estimatedTokenCost = computed(() => {
  const input = priceTokens(inputTokensTotal.value, TOKEN_PRICE_PER_MILLION.input);
  const output = priceTokens(completionTokensTotal.value, TOKEN_PRICE_PER_MILLION.output);
  const cacheRead = priceTokens(cacheReadTokensTotal.value, TOKEN_PRICE_PER_MILLION.cacheRead);
  const cacheWrite = priceTokens(cacheWriteTokensTotal.value, TOKEN_PRICE_PER_MILLION.cacheWrite);
  return {
    input,
    output,
    cacheRead,
    cacheWrite,
    total: input + output + cacheRead + cacheWrite,
  };
});

const cacheTooltipContent = computed(() => {
  const formula = includeCacheWriteInHitRate.value
    ? "缓存读取 /（缓存读取 + 缓存创建 + 非缓存输入）"
    : "缓存读取 /（缓存读取 + 非缓存输入）";
  return [
    `当前：${formatRateLabel(selectedCacheHitRate.value)}`,
    `公式：${formula}`,
    `默认 ${formatRateLabel(defaultCacheHitRate.value)} / 计入创建 ${formatRateLabel(cacheReuseRate.value)}`,
  ].join("\n");
});

const turnsTooltipContent = computed(() =>
  [
    "按历史记录里扫描到的回合 summary 汇总。",
    "",
    `总轮次：${formatMetricValue(props.metrics?.turnsTotal)}`,
    `有效轮次：${formatMetricValue(props.metrics?.validTurnsTotal)}`,
    `异常轮次：${formatMetricValue(props.metrics?.invalidTurnsTotal)}`,
    `有效占比：${formatRateLabel(validTurnsRate.value)}`,
  ].join("\n"),
);

const tokensTooltipContent = computed(() =>
  [
    "总请求 Token 包含 Prompt 和模型输出。",
    "",
    `总请求：${formatMetricValue(props.metrics?.requestTokensTotal)}`,
    `Prompt：${formatMetricValue(props.metrics?.promptTokensTotal)}`,
    `输出推算：${formatMetricValue(completionTokensTotal.value)}`,
    `非缓存输入：${formatMetricValue(inputTokensTotal.value)}`,
    `缓存读取：${formatMetricValue(cacheReadTokensTotal.value)}`,
    `缓存写入：${formatMetricValue(cacheWriteTokensTotal.value)}`,
    "",
    "缓存读写已计入 Prompt 侧统计。",
  ].join("\n"),
);

const costTooltipContent = computed(() =>
  [
    "按 Claude Opus 4.7 价格估算。",
    `缓存统计策略：${selectedCacheRateModeLabel.value}（${formatRateLabel(selectedCacheHitRate.value)}）`,
    "",
    `普通输入：${formatMetricValue(inputTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.input}/1M = ${formatUSD(estimatedTokenCost.value.input)}`,
    `模型输出：${formatMetricValue(completionTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.output}/1M = ${formatUSD(estimatedTokenCost.value.output)}`,
    `缓存读取：${formatMetricValue(cacheReadTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.cacheRead}/1M = ${formatUSD(estimatedTokenCost.value.cacheRead)}`,
    `缓存写入：${formatMetricValue(cacheWriteTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.cacheWrite}/1M = ${formatUSD(estimatedTokenCost.value.cacheWrite)}`,
    "",
    `合计：${formatUSD(estimatedTokenCost.value.total)}`,
  ].join("\n"),
);

function normalizeHomeAd(item, index) {
  const source = item && typeof item === "object" ? item : {};
  const title = typeof source.title === "string" ? source.title.trim() : "";
  if (!title) {
    return null;
  }
  return {
    id: typeof source.id === "string" && source.id.trim() ? source.id.trim() : String(index + 1),
    title,
    subtitle: typeof source.subtitle === "string" ? source.subtitle.trim() : "",
  };
}

async function toggleIncludeCacheWriteInHitRate(value) {
  const nextValue = Boolean(value);
  homeMetricsConfigSaving.value = true;
  homeMetricsConfigError.value = "";
  try {
    const result = await saveIncludeCacheWriteInHitRate(nextValue);
    if (!result?.ok) {
      homeMetricsConfigError.value = result?.error || "保存失败";
    }
  } catch (error) {
    homeMetricsConfigError.value = error?.message || "保存失败";
  } finally {
    homeMetricsConfigSaving.value = false;
  }
}

const normalizedHomeAds = computed(() => {
  const list = Array.isArray(props.homeAds) && props.homeAds.length > 0 ? props.homeAds : [props.homeAd];
  return list.map(normalizeHomeAd).filter(Boolean);
});

const hasHomeAd = computed(() => normalizedHomeAds.value.length > 0);

// ===== 视图切换 =====
//
// 总览与趋势互斥切换：前者回答「累计花了多少」，后者回答「什么时候用得多」。
// 趋势区（196px）比总览区（130px）高 66px，差值由 useWindowGrow 同步加高主窗口，
// 下方的服务卡片与「本地配置」不会被挤压；切回总览时窗口恢复原高。
const HOME_VIEW_TABS = [
  { value: "summary", label: "总览" },
  { value: "trend", label: "趋势" },
];

const TREND_EXTRA_HEIGHT = 66;

// 后端 LoadUsageSeries 只接受「回溯 N 个自然日（含今天）」，所以自定义也是天数，不是日历起止。
const CUSTOM_DAYS_MIN = 1;
const CUSTOM_DAYS_MAX = 365;

// 14 天窗口里饼图没有意义（它回答「构成」，而趋势 tab 回答「随时间怎么变」），
// 所以这里只给柱/折线两种。状态只存在于组件内，不落盘。
const TREND_CHART_KINDS = [
  { value: "bar", label: "柱状图", icon: "icon-[mdi--chart-bar]" },
  { value: "line", label: "折线图", icon: "icon-[mdi--chart-line]" },
];

// 堆叠维度回答三个不同问题：Token 分层看成本结构，中转站看来源分布，模型看谁在吃预算。
// 数据侧无需改动：syncUsageSeries 返回的每一天已带 providers / models 明细。
const TREND_DIMENSIONS = [
  { value: "token", label: "Token 分层", icon: "icon-[mdi--layers-outline]" },
  { value: "provider", label: "按中转站", icon: "icon-[mdi--server-network]" },
  { value: "model", label: "按模型", icon: "icon-[mdi--robot-outline]" },
];

const activeView = ref("summary");
const trendChartKind = ref("bar");
const trendDimension = ref("token");
const trendRange = ref("14");
const trendCustomDays = ref(21);
const trendLoaded = ref(false);

function effectiveTrendDays() {
  if (trendRange.value === "custom") {
    const days = Math.round(Number(trendCustomDays.value));
    if (!Number.isFinite(days)) {
      return 14;
    }
    return Math.min(CUSTOM_DAYS_MAX, Math.max(CUSTOM_DAYS_MIN, days));
  }
  const preset = Number(trendRange.value);
  return Number.isFinite(preset) && preset > 0 ? preset : 14;
}

function loadTrendSeries() {
  return syncUsageSeries(effectiveTrendDays());
}

// 预设与自定义共用同一条落库路径：只改「当前生效的天数口径」，再统一拉一次序列。
async function handleTrendRangeSelect(value) {
  if (value === trendRange.value) {
    return;
  }
  trendRange.value = value;
  trendLoaded.value = true;
  await loadTrendSeries();
}

async function handleCustomDaysApply(days) {
  const parsed = Math.round(Number(days));
  if (!Number.isFinite(parsed)) {
    return;
  }
  if (trendRange.value === "custom" && parsed === trendCustomDays.value) {
    return;
  }
  trendCustomDays.value = parsed;
  trendRange.value = "custom";
  trendLoaded.value = true;
  await loadTrendSeries();
}

const usageDays = computed(() => appState.usageSeries.days ?? []);
const usageLoading = computed(() => appState.usageSeriesLoading);
const usageError = computed(() => appState.usageSeriesError);

function sumTokens(days) {
  return days.reduce((sum, day) => sum + normalizeNumber(day?.totalTokens), 0);
}

// 环比：把窗口对半切，用后半段比前半段。回答「最近是在多用还是少用」——
// 这是原来四个纯累计读数完全无法表达的信息。
// 前半段为 0 时不给百分比：任何数除以 0 的「+∞%」都是噪声。
function buildMomentum(days) {
  if (days.length < 4) {
    return { direction: "flat", percent: null };
  }
  const split = Math.floor(days.length / 2);
  const earlier = sumTokens(days.slice(0, split));
  const later = sumTokens(days.slice(split));
  if (earlier <= 0) {
    return { direction: later > 0 ? "up" : "flat", percent: null };
  }
  const ratio = (later - earlier) / earlier;
  if (Math.abs(ratio) < 0.02) {
    return { direction: "flat", percent: 0 };
  }
  return { direction: ratio > 0 ? "up" : "down", percent: Math.abs(ratio) * 100 };
}

const trendMetrics = computed(() => {
  const days = usageDays.value;
  const totalTokens = sumTokens(days);
  const totalCalls = days.reduce((sum, day) => sum + normalizeNumber(day?.providerCalls), 0);
  const peak = days.reduce(
    (best, day) =>
      normalizeNumber(day?.totalTokens) > best.value
        ? { date: String(day?.date || ""), value: normalizeNumber(day?.totalTokens) }
        : best,
    { date: "", value: 0 },
  );
  const cacheRead = days.reduce((sum, day) => sum + normalizeNumber(day?.cacheReadTokens), 0);
  const input = days.reduce((sum, day) => sum + normalizeNumber(day?.inputTokens), 0);
  return {
    totalTokens,
    totalCalls,
    dailyAverage: days.length > 0 ? Math.round(totalTokens / days.length) : 0,
    peakLabel: peak.date ? `${peak.date.slice(5).replace("-", "/")} · ${formatCompactInteger(peak.value)}` : "-",
    momentum: buildMomentum(days),
    // 日均被零调用日拉低时，活跃天数正是那个解释。
    activeDays: days.filter((day) => normalizeNumber(day?.totalTokens) > 0).length,
    windowDays: days.length,
    // 与总览 tab 的默认口径保持一致：缓存读取 /（缓存读取 + 非缓存输入）。
    cacheHitRate: calculateRate(cacheRead, cacheRead + input),
  };
});

const momentumStyle = computed(() => {
  const { direction, percent } = trendMetrics.value.momentum;
  if (direction === "up") {
    return {
      icon: "icon-[mdi--trending-up]",
      color: "text-[#86efac]",
      text: percent === null ? "新增用量" : `较前期 +${percent.toFixed(0)}%`,
    };
  }
  if (direction === "down") {
    return {
      icon: "icon-[mdi--trending-down]",
      color: "text-[#fca5a5]",
      text: `较前期 -${percent.toFixed(0)}%`,
    };
  }
  return { icon: "icon-[mdi--trending-neutral]", color: "text-[#8a8a8a]", text: "较前期持平" };
});

// DailyUsageChart 在 compact 下不渲染图例（那会吃掉本就紧张的 196px）。
// 但按中转站/按模型堆叠时，色块不配名字就完全无法辨认，
// 所以这里复算一份同源序列，塞进底部那一行本来放「活跃天数 / 缓存命中」的位置。
const TREND_LEGEND_LIMIT = 4;

const trendLegend = computed(() => {
  if (trendDimension.value === "token") {
    return { items: [], overflow: 0 };
  }
  const series = buildUsageSeries(usageDays.value, {
    dimension: trendDimension.value,
    metric: "totalTokens",
  });
  return {
    items: series.slice(0, TREND_LEGEND_LIMIT),
    overflow: Math.max(0, series.length - TREND_LEGEND_LIMIT),
  };
});

// 趋势数据按需拉取：默认停在总览时不付出这次 IPC。
const { grow: growMainWindow, restore: restoreMainWindow } = useWindowGrow();

watch(
  activeView,
  (view) => {
    if (view === "trend") {
      void growMainWindow(TREND_EXTRA_HEIGHT);
      if (!trendLoaded.value) {
        trendLoaded.value = true;
        void loadTrendSeries();
      }
      return;
    }
    void restoreMainWindow();
  },
  { immediate: true },
);

function handleRefresh() {
  emit("refresh");
  if (activeView.value === "trend") {
    void loadTrendSeries();
  }
}
</script>

<template>
  <div>
    <div class="flex flex-col gap-4">
      <div class="flex items-center justify-between gap-4 h-[42px]">
        <div class="center-row min-w-0 justify-start gap-1 shrink-0">
          <button
            v-for="tab in HOME_VIEW_TABS"
            :key="tab.value"
            type="button"
            class="rounded-[6px] border px-2.5 py-1 text-xs transition-colors duration-150"
            :class="activeView === tab.value
              ? 'border-[#4a4a4a] bg-[#2f2f2f] text-white'
              : 'border-transparent text-[#8a8a8a] hover:text-[#d4d4d4]'"
            :title="tab.value === 'trend' ? `近 ${effectiveTrendDays()} 天用量趋势` : ''"
            @click="activeView = tab.value"
          >
            {{ tab.label }}
          </button>
          <!-- 形态 | 维度 | 区间：三组语义不同，用竖线分隔，避免读成一排互斥按钮。 -->
          <span v-if="activeView === 'trend'" class="center-row ml-1.5 gap-0.5">
            <button
              v-for="kind in TREND_CHART_KINDS"
              :key="kind.value"
              type="button"
              class="center-row justify-center h-[22px] w-[22px] rounded-[5px] border transition-interactive duration-150"
              :class="trendChartKind === kind.value
                ? 'border-[#4a4a4a] bg-[#2f2f2f] text-white'
                : 'border-transparent text-[#6f6f6f] hover:text-[#d4d4d4]'"
              :aria-pressed="trendChartKind === kind.value"
              :title="kind.label"
              @click="trendChartKind = kind.value"
            >
              <span :class="[kind.icon, 'text-[13px]']"></span>
            </button>
            <span class="mx-1 h-[14px] w-px shrink-0 bg-[#3a3a3a]"></span>
            <button
              v-for="item in TREND_DIMENSIONS"
              :key="item.value"
              type="button"
              class="center-row justify-center h-[22px] w-[22px] rounded-[5px] border transition-interactive duration-150"
              :class="trendDimension === item.value
                ? 'border-[#4a4a4a] bg-[#2f2f2f] text-white'
                : 'border-transparent text-[#6f6f6f] hover:text-[#d4d4d4]'"
              :aria-pressed="trendDimension === item.value"
              :title="item.label"
              @click="trendDimension = item.value"
            >
              <span :class="[item.icon, 'text-[13px]']"></span>
            </button>
            <span class="mx-1 h-[14px] w-px shrink-0 bg-[#3a3a3a]"></span>
            <TrendRangePicker
              :range="trendRange"
              :custom-days="trendCustomDays"
              :min="CUSTOM_DAYS_MIN"
              :max="CUSTOM_DAYS_MAX"
              aria-label="统计区间"
              @select="handleTrendRangeSelect"
              @apply="handleCustomDaysApply"
            />
          </span>
        </div>
        <div v-if="hasHomeAd" class="grid min-w-0  grid-cols-3 gap-2 shrink-0">
          <div
            v-for="ad in normalizedHomeAds"
            :key="ad.id"
            style="font-family: var(--font-num)"
            class="center-row h-[42px] min-w-0 cursor-pointer gap-[8px] rounded-[6px] border border-[#343434] bg-[#242424] px-[8px] pr-[10px] text-left transition-colors duration-150 hover:border-[#4a4a4a] hover:bg-[#2a2a2a] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-400/50"
            role="button"
            tabindex="0"
            :title="ad.subtitle ? `${ad.title}\n${ad.subtitle}` : ad.title"
            @click="emit('open-ad', ad.id)"
            @keydown.enter.prevent="emit('open-ad', ad.id)"
            @keydown.space.prevent="emit('open-ad', ad.id)"
          >
            <div
              class="center-row h-[20px] w-[20px] shrink-0 justify-center text-[20px] text-amber-400"
            >
              <span class="icon-[cil--badge]"></span>
            </div>
            <div class="min-w-0 flex-1">
              <div class="truncate text-[13px] font-medium leading-[16px] text-white">
                {{ ad.title }}
              </div>
              <div
                v-if="ad.subtitle"
                class="mt-[2px] center-row min-w-0 gap-[2px] text-[11px] leading-[12px] text-[#8A8A8A]"
              >
                <span class="truncate">{{ ad.subtitle }}</span>
              </div>
            </div>
          </div>
        </div>
        <div
          class="flex-1 center-row justify-end shrink-0 gap-1.5 text-xs text-[#6f6f6f]"
        >
          <button
            type="button"
            class="center-row h-[24px] gap-1 rounded-[6px] border border-[#3b3b3b] bg-[#242424] px-2 text-[11px] text-[#9d9d9d] transition-colors duration-150 hover:border-[#4c4c4c] hover:text-white"
            title="中转配置"
            aria-label="中转配置"
            @click="emit('open-provider-config')"
          >
            <span class="icon-[mdi--tune-vertical] text-[14px]"></span>
            <span>中转配置</span>
          </button>
          <button
            type="button"
            class="center-row h-[24px] gap-1 rounded-[6px] border border-[#3b3b3b] bg-[#242424] px-2 text-[11px] text-[#9d9d9d] transition-colors duration-150 hover:border-[#4c4c4c] hover:text-white"
            title="调用统计"
            aria-label="调用统计"
            @click="emit('open-usage-stats')"
          >
            <span class="icon-[mdi--chart-box-outline] text-[14px]"></span>
            <span>调用统计</span>
          </button>
          <button
            type="button"
            class="center-row justify-center h-[24px] w-[24px] rounded-[6px] border border-[#3b3b3b] bg-[#242424] text-[#9d9d9d] transition-colors duration-150 hover:border-[#4c4c4c] hover:text-white disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="loading || usageLoading"
            :title="loading || usageLoading ? '刷新中' : '刷新统计'"
            aria-label="刷新统计"
            @click="handleRefresh"
          >
            <span
              class="icon-[mdi--refresh] text-[14px]"
              :class="{ '!animate-spin': loading || usageLoading }"
            ></span>
          </button>
        </div>
      </div>

      <div
        class="mt-[-4px] overflow-hidden rounded-[8px] border border-[#343434] bg-[#242424] transition-[height] duration-200 ease-out"
        :class="activeView === 'trend' ? 'h-[196px]' : 'h-[130px]'"
      >
        <Transition name="home-view" mode="out-in">
        <div v-if="activeView === 'trend'" key="trend" class="flex h-full flex-col px-3 pb-2 pt-2">
          <div
            v-if="usageError"
            class="flex h-full flex-col items-center justify-center gap-2 text-xs text-[#fca5a5]"
          >
            <span class="center-row min-w-0 gap-1.5">
              <span class="icon-[mdi--alert-circle-outline] shrink-0 text-[14px]"></span>
              <span class="truncate">{{ usageError }}</span>
            </span>
            <button
              type="button"
              class="rounded-[6px] border border-[#5b2626] px-2 py-0.5 transition-colors duration-150 hover:bg-[#3a1a1a] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="usageLoading"
              @click="handleRefresh"
            >
              重试
            </button>
          </div>
          <template v-else>
            <!-- 摘要区做主次分层：总 Token 是主读数，其余是解释它的次级信息。
                 原来四个数字同字号平铺，视觉上等权，读者不知道该先看哪个。 -->
            <div class="center-row shrink-0 items-end justify-between gap-3 border-b border-[#343434] pb-1.5">
              <div class="min-w-0 center-row items-end justify-start gap-2">
                <span
                  class="truncate text-[20px] leading-none text-white"
                  style="font-family: var(--font-num)"
                  :title="formatInteger(trendMetrics.totalTokens)"
                >
                  {{ formatCompactInteger(trendMetrics.totalTokens) }}
                </span>
                <span class="shrink-0 pb-[1px] text-[11px] text-[#737373]">Token</span>
                <span
                  class="center-row shrink-0 gap-0.5 pb-[1px] text-[11px]"
                  :class="momentumStyle.color"
                >
                  <span :class="[momentumStyle.icon, 'text-[13px]']"></span>
                  <span>{{ momentumStyle.text }}</span>
                </span>
              </div>
              <div class="center-row shrink-0 gap-3 pb-[1px] text-[11px] text-[#737373]">
                <span>调用 <span class="text-[#d4d4d4]">{{ formatInteger(trendMetrics.totalCalls) }}</span></span>
                <span>日均 <span class="text-[#d4d4d4]" :title="formatInteger(trendMetrics.dailyAverage)">{{ formatCompactInteger(trendMetrics.dailyAverage) }}</span></span>
                <span>峰值 <span class="text-[#d4d4d4]">{{ trendMetrics.peakLabel }}</span></span>
              </div>
            </div>
            <div class="min-h-0 flex-1 pt-1">
              <DailyUsageChart
                :days="usageDays"
                metric="totalTokens"
                :dimension="trendDimension"
                :chart-type="trendChartKind"
                compact
              />
            </div>
            <!-- 同一行两种角色：Token 分层语义固定、用户认得，让位给活跃度与命中率；
                 分组维度下色块不配名字读不出来，图例的信息价值更高。 -->
            <div class="center-row shrink-0 justify-between gap-3 pt-1 text-[11px] text-[#6f6f6f]">
              <template v-if="trendLegend.items.length > 0">
                <span class="center-row min-w-0 flex-nowrap gap-2.5 overflow-hidden">
                  <span
                    v-for="item in trendLegend.items"
                    :key="item.id"
                    class="center-row min-w-0 shrink-0 gap-1"
                    :title="item.name"
                  >
                    <span class="h-2 w-2 shrink-0 rounded-[2px]" :style="{ backgroundColor: item.color }"></span>
                    <span class="max-w-[92px] truncate text-[#a3a3a3]">{{ item.name }}</span>
                  </span>
                  <span v-if="trendLegend.overflow > 0" class="shrink-0">+{{ trendLegend.overflow }}</span>
                </span>
                <span class="shrink-0">
                  活跃 <span class="text-[#a3a3a3]">{{ trendMetrics.activeDays }}</span> / {{ trendMetrics.windowDays }}
                </span>
              </template>
              <template v-else>
                <span>
                  活跃 <span class="text-[#a3a3a3]">{{ trendMetrics.activeDays }}</span> / {{ trendMetrics.windowDays }} 天
                </span>
                <span>
                  缓存命中 <span class="text-[#a3a3a3]">{{ formatRateLabel(trendMetrics.cacheHitRate) }}</span>
                </span>
              </template>
            </div>
          </template>
        </div>

        <div v-else key="summary" class="grid h-full grid-cols-4 gap-0">
        <div class="min-w-0 px-4 py-4 flex flex-col justify-between">
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>缓存命中率</span>
            <Tooltip>
              <div class="w-[280px] space-y-3">
                <div class="border-b border-[#343434] pb-3">
                  <Switch
                    compact
                    label="计入缓存创建"
                    description="开启后把缓存创建纳入分母"
                    enabled-text="当前按复用率口径显示"
                    disabled-text="当前按默认命中率口径显示"
                    :enabled="includeCacheWriteInHitRate"
                    :busy="homeMetricsConfigSaving"
                    :disabled="homeMetricsConfigSaving"
                    @change="toggleIncludeCacheWriteInHitRate"
                  />
                </div>
                <div class="whitespace-pre-wrap">{{ cacheTooltipContent }}</div>
                <div v-if="homeMetricsConfigError" class="text-[11px] text-[#f87171]">
                  {{ homeMetricsConfigError }}
                </div>
              </div>
            </Tooltip>
          </div>
          <CacheHitRateChart :rate="selectedCacheHitRate" />
        </div>

        <div
          class="min-w-0 border-l border-[#343434] px-4 py-4 flex flex-col justify-between"
        >
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>对话轮次</span>
            <Tooltip :content="turnsTooltipContent" />
          </div>
          <div>
            <div
              class="text-[30px] leading-none text-white"
              style="font-family: var(--font-num)"
              :title="formatInteger(metrics.turnsTotal)"
            >
              {{ formatCompactInteger(metrics.turnsTotal) }}
            </div>
            <div class="mt-3 text-xs leading-5 text-[#8c8c8c]">
              有效
              <span :title="formatInteger(metrics.validTurnsTotal)">
                {{ formatCompactInteger(metrics.validTurnsTotal) }}
              </span>
              / 异常
              <span :title="formatInteger(metrics.invalidTurnsTotal)">
                {{ formatCompactInteger(metrics.invalidTurnsTotal) }}
              </span>
            </div>
          </div>
        </div>

        <div
          class="min-w-0 border-l border-[#343434] px-4 py-4 flex flex-col justify-between"
        >
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>Token 消耗</span>
            <Tooltip :content="tokensTooltipContent" />
          </div>
          <div>
            <div
              class="truncate text-[30px] leading-none text-white"
              style="font-family: var(--font-num)"
              :title="formatInteger(metrics.requestTokensTotal)"
            >
              {{ formatCompactInteger(metrics.requestTokensTotal) }}
            </div>
            <div class="mt-3 text-xs leading-5 text-[#8c8c8c]">
              Prompt
              <span :title="formatInteger(metrics.promptTokensTotal)">
                {{ formatCompactInteger(metrics.promptTokensTotal) }}
              </span>
            </div>
          </div>
        </div>

        <div
          class="min-w-0 border-l border-[#343434] px-4 py-4 flex flex-col justify-between"
        >
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>价值估算</span>
            <Tooltip :content="costTooltipContent" />
          </div>
          <div>
            <div
              class="truncate text-[30px] leading-none text-white"
              style="font-family: var(--font-num)"
              :title="formatUSD(estimatedTokenCost.total)"
            >
              {{ formatUSD(estimatedTokenCost.total) }}
            </div>
            <div class="mt-3 text-xs leading-5 text-[#8c8c8c]">
              缓存读写
              <span :title="formatUSD(estimatedTokenCost.cacheRead + estimatedTokenCost.cacheWrite)">
                {{ formatUSD(estimatedTokenCost.cacheRead + estimatedTokenCost.cacheWrite) }}
              </span>
            </div>
          </div>
        </div>
        </div>
        </Transition>
      </div>
    </div>
  </div>
</template>

<style scoped>
.home-view-enter-active,
.home-view-leave-active {
  transition: opacity 140ms ease, transform 180ms ease;
}

.home-view-enter-from {
  opacity: 0;
  transform: translateY(3px);
}

.home-view-leave-to {
  opacity: 0;
  transform: translateY(-2px);
}
</style>
