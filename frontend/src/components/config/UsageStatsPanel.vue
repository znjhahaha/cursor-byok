<script setup>
import DailyUsageChart from "@/components/charts/DailyUsageChart.vue";
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import { appState, syncUsageSeries } from "@/state/appState";
import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";
import { computed, onMounted, ref, watch } from "vue";

const rangeOptions = [
  { label: "最近 7 天", value: "7" },
  { label: "最近 14 天", value: "14" },
  { label: "最近 30 天", value: "30" },
  { label: "最近 90 天", value: "90" },
];

const metricOptions = [
  { label: "总 Token", value: "totalTokens", icon: "icon-[mdi--counter]" },
  { label: "调用次数", value: "providerCalls", icon: "icon-[mdi--api]" },
];

// 三种堆叠维度回答三个不同问题：Token 类型看成本结构，中转站看来源分布，模型看谁在吃预算。
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

const range = ref("30");
const metric = ref("totalTokens");
const dimension = ref("token");
const chartType = ref("bar");

// 下钻锚点：从「按中转站」点进某一站后，视图收窄为该站内部的模型构成。
// 它只是一层过滤，不改变数据来源，因此清除即可完整回到全局视图。
const focusProviderID = ref("");

const days = computed(() => appState.usageSeries.days ?? []);
const providers = computed(() => appState.usageSeries.providers ?? []);
const models = computed(() => appState.usageSeries.models ?? []);

const focusProviderName = computed(
  () =>
    providers.value.find((item) => item.providerID === focusProviderID.value)?.providerName ??
    focusProviderID.value,
);

// 过滤在面板层完成，图表只负责渲染传入的序列，不需要理解下钻语义。
const chartDays = computed(() => {
  if (!focusProviderID.value || dimension.value !== "model") {
    return days.value;
  }
  return days.value.map((day) => {
    const provider = (day.providers ?? []).find(
      (item) => item.providerID === focusProviderID.value,
    );
    return {
      ...day,
      totalTokens: Number(provider?.totalTokens ?? 0),
      providerCalls: Number(provider?.providerCalls ?? 0),
      models: (day.models ?? []).filter((item) => item.providerID === focusProviderID.value),
    };
  });
});

const metricLabel = computed(
  () => metricOptions.find((item) => item.value === metric.value)?.label ?? "",
);

// 调用次数没有 Token 分层语义，此时把维度选择器降级为按来源/模型，避免选出一张单柱图。
const availableDimensions = computed(() =>
  metric.value === "totalTokens"
    ? dimensionOptions
    : dimensionOptions.filter((item) => item.value !== "token"),
);

watch(metric, (value) => {
  if (value !== "totalTokens" && dimension.value === "token") {
    dimension.value = "provider";
  }
});

function formatValue(value) {
  return metric.value === "providerCalls"
    ? formatInteger(value)
    : formatCompactInteger(value);
}

// formatCompactInteger 会丢精度，title 里补一份完整值供核对。
function exactValue(value) {
  return formatInteger(value);
}

// 排行榜跟随堆叠维度切换，保证图与表读的是同一份口径。
const rankingConfig = computed(() =>
  dimension.value === "model"
    ? { title: "按模型", source: models.value, idKey: "model", nameKey: "model", empty: "暂无按模型的统计数据" }
    : { title: "按中转站", source: providers.value, idKey: "providerID", nameKey: "providerName", empty: "暂无按中转站的统计数据" },
);

const rankedItems = computed(() => {
  const { source, idKey, nameKey } = rankingConfig.value;
  const scoped =
    dimension.value === "model" && focusProviderID.value
      ? source.filter((item) => item.providerID === focusProviderID.value)
      : source;
  const total = scoped.reduce((sum, item) => sum + Number(item[metric.value] ?? 0), 0);
  return scoped
    .map((item) => {
      const value = Number(item[metric.value] ?? 0);
      return {
        id: String(item[idKey] ?? ""),
        name: String(item[nameKey] || item[idKey] || "未知"),
        // 模型行额外展示来源站点，回答「这个模型走的哪个站」。
        hint: dimension.value === "model" ? String(item.providerName || "") : "",
        value,
        share: total > 0 ? value / total : 0,
      };
    })
    .filter((item) => item.id)
    .sort((left, right) => right.value - left.value);
});

const canDrillDown = computed(() => dimension.value !== "model");

// 点击某个中转站 = 「我想看这一站里面是什么」，因此同时切维度并设置锚点。
function handleDrillDown(item) {
  if (!canDrillDown.value) {
    return;
  }
  focusProviderID.value = item.id;
  dimension.value = "model";
}

function clearFocus() {
  focusProviderID.value = "";
  dimension.value = "provider";
}

watch(dimension, (value) => {
  if (value !== "model") {
    focusProviderID.value = "";
  }
});

async function refresh() {
  await syncUsageSeries(Number(range.value));
}

watch(range, refresh);

onMounted(refresh);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-3">
    <div class="flex shrink-0 items-center justify-between gap-3">
      <div class="center-row gap-2">
        <Select v-model="range" :options="rangeOptions" aria-label="统计区间" :border="false" />
        <Select v-model="metric" :options="metricOptions" aria-label="统计指标" :border="false" />
        <Select
          v-model="dimension"
          :options="availableDimensions"
          aria-label="堆叠维度"
          :border="false"
        />
      </div>
      <div class="center-row shrink-0 gap-2">
        <div class="center-row rounded-[7px] border border-[#343434] bg-[#232323] p-0.5">
          <button
            v-for="item in chartTypeOptions"
            :key="item.value"
            type="button"
            class="center-row h-7 w-8 justify-center rounded-[5px] text-[#7f7f7f] transition-colors duration-150"
            :class="chartType === item.value ? 'bg-[#343434] text-[#86efac]' : 'hover:text-[#d4d4d4]'"
            :title="item.label"
            @click="chartType = item.value"
          >
            <span :class="[item.icon, 'text-[15px]']"></span>
          </button>
        </div>
        <Button variant="default" :disabled="appState.usageSeriesLoading" @click="refresh">
          {{ appState.usageSeriesLoading ? "刷新中..." : "刷新" }}
        </Button>
      </div>
    </div>

    <div
      v-if="appState.usageSeriesError"
      class="center-row shrink-0 justify-between gap-3 rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]"
    >
      <span class="center-row min-w-0 gap-1.5">
        <span class="icon-[mdi--alert-circle-outline] shrink-0 text-[16px]"></span>
        <span class="truncate" :title="appState.usageSeriesError">
          {{ appState.usageSeriesError }}
        </span>
      </span>
      <button
        type="button"
        class="shrink-0 rounded-[6px] border border-[#5b2626] px-2 py-0.5 text-xs transition-colors duration-150 hover:bg-[#3a1a1a] disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="appState.usageSeriesLoading"
        @click="refresh"
      >
        重试
      </button>
    </div>

    <div class="min-h-[220px] flex-1 rounded-[8px] border border-[#343434] bg-[#252525] p-3">
      <DailyUsageChart
        :days="chartDays"
        :metric="metric"
        :dimension="dimension"
        :chart-type="chartType"
      />
    </div>

    <div class="shrink-0 rounded-[8px] border border-[#343434] bg-[#252525] p-3">
      <div class="flex items-center justify-between gap-3 pb-2">
        <span class="center-row min-w-0 gap-2 text-sm text-[#d4d4d4]">
          <span class="truncate">{{ rankingConfig.title }} · {{ metricLabel }}</span>
          <button
            v-if="focusProviderID"
            type="button"
            class="center-row shrink-0 gap-1 rounded-[999px] border border-[#1ca35a] bg-[#123322] px-2 py-0.5 text-[11px] text-[#86efac] transition-colors duration-150 hover:bg-[#164028]"
            title="返回全部中转站"
            @click="clearFocus"
          >
            <span class="truncate max-w-[140px]">{{ focusProviderName }}</span>
            <span class="icon-[ic--round-close] text-[12px]"></span>
          </button>
        </span>
        <span class="text-xs text-[#737373]">{{ rankedItems.length }} 项</span>
      </div>

      <div v-if="rankedItems.length === 0" class="py-4 text-center text-sm text-[#a3a3a3]">
        {{ rankingConfig.empty }}
      </div>

      <div v-else class="usage-ranking-scroll flex max-h-[160px] flex-col gap-2 overflow-y-auto pr-1">
        <div
          v-for="item in rankedItems"
          :key="item.id"
          class="flex flex-col gap-1 rounded-[6px] px-1 py-0.5 transition-colors duration-150"
          :class="canDrillDown ? 'cursor-pointer hover:bg-[#2f2f2f]' : ''"
          :title="canDrillDown ? `查看 ${item.name} 的模型构成` : ''"
          @click="handleDrillDown(item)"
        >
          <div class="flex items-center justify-between gap-3 text-sm">
            <span class="center-row min-w-0 gap-2">
              <span class="truncate text-[#d4d4d4]" :title="item.name">{{ item.name }}</span>
              <span
                v-if="item.hint"
                class="shrink-0 rounded-[4px] bg-[#2f2f2f] px-1.5 py-0.5 text-[11px] text-[#8f8f8f]"
              >
                {{ item.hint }}
              </span>
            </span>
            <span class="shrink-0 font-num text-[#e5e5e5]" :title="exactValue(item.value)">
              {{ formatValue(item.value) }}
            </span>
          </div>
          <div class="h-1.5 overflow-hidden rounded-[999px] bg-[#1f1f1f]">
            <div
              class="h-full rounded-[999px] bg-[#1ca35a]"
              :style="{ width: `${(item.share * 100).toFixed(1)}%` }"
            ></div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.usage-ranking-scroll {
  scrollbar-width: thin;
  scrollbar-color: #4a4a4a transparent;
}

.usage-ranking-scroll::-webkit-scrollbar {
  width: 6px;
}

.usage-ranking-scroll::-webkit-scrollbar-thumb {
  border-radius: 999px;
  background: #4a4a4a;
}
</style>