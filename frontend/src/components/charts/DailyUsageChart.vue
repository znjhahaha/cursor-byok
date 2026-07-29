<script setup>
import { createExternalTooltip } from "@/components/charts/externalTooltip";
import {
  METRIC_LABELS,
  asUsageNumber,
  buildShareSeries,
  buildUsageSeries,
  formatUsageValue,
} from "@/components/charts/usageSeries";
import { formatInteger } from "@/utils/numberFormat";
import {
  ArcElement,
  BarController,
  BarElement,
  CategoryScale,
  Chart as ChartJS,
  Legend,
  LinearScale,
  LineController,
  LineElement,
  PieController,
  PointElement,
  Tooltip,
} from "chart.js";
import { computed, onBeforeUnmount, onDeactivated } from "vue";
import { Chart } from "vue-chartjs";

ChartJS.register(
  ArcElement,
  BarController,
  BarElement,
  CategoryScale,
  Legend,
  LinearScale,
  LineController,
  LineElement,
  PieController,
  PointElement,
  Tooltip,
);

const props = defineProps({
  days: { type: Array, default: () => [] },
  metric: { type: String, default: "totalTokens" },
  dimension: { type: String, default: "token" },
  chartType: {
    type: String,
    default: "bar",
    validator: (value) => ["bar", "line", "pie"].includes(value),
  },
  compact: { type: Boolean, default: false },
});

const externalTooltip = createExternalTooltip();
onBeforeUnmount(externalTooltip.destroy);
// 外部 tooltip 是挂在 body 上的游离 DOM。父面板被 KeepAlive 缓存时不会 unmount，
// 若切走那一刻指针正停在图上，就会留下一个摘不掉的孤儿 tooltip。
onDeactivated(externalTooltip.destroy);

function parseDate(value) {
  const text = String(value || "").trim();
  if (!/^\d{4}-\d{2}-\d{2}$/.test(text)) {
    return null;
  }
  const date = new Date(`${text}T00:00:00`);
  return Number.isNaN(date.getTime()) ? null : date;
}

function axisLabel(value) {
  const date = parseDate(value);
  if (!date) {
    return String(value || "");
  }
  const weekday = date.getDay();
  if (weekday === 6) return "周六";
  if (weekday === 0) return "周日";
  return `${date.getMonth() + 1}/${date.getDate()}`;
}

function tooltipTitle(value) {
  const date = parseDate(value);
  return date ? `${date.getMonth() + 1}/${date.getDate()}` : String(value || "");
}

function formatDetailedValue(value) {
  const compact = formatUsageValue(value, props.metric);
  const exact = formatInteger(value);
  return compact === exact ? exact : `${compact}（${exact}）`;
}

const series = computed(() =>
  buildUsageSeries(props.days, { dimension: props.dimension, metric: props.metric }),
);
const shareSeries = computed(() =>
  buildShareSeries(props.days, { dimension: props.dimension, metric: props.metric }),
);
const hasData = computed(() =>
  series.value.some((item) => item.values.some((value) => value > 0)),
);
const chartKind = computed(() => props.chartType);

const chartData = computed(() => {
  if (props.chartType === "pie") {
    return {
      labels: shareSeries.value.map((item) => item.name),
      datasets: [
        {
          data: shareSeries.value.map((item) => item.value),
          backgroundColor: shareSeries.value.map((item) => item.color),
          borderColor: "#252525",
          borderWidth: 2,
          hoverOffset: 6,
        },
      ],
    };
  }

  const isSingleDay = props.days.length === 1;
  const singleDayPointRadius = (context, radius) =>
    isSingleDay && asUsageNumber(context.raw) > 0 ? radius : 0;

  return {
    labels: props.days.map((day) => day?.date ?? ""),
    datasets: series.value.map((item) =>
      props.chartType === "line"
        ? {
            label: item.name,
            data: item.values,
            borderColor: item.color,
            backgroundColor: item.color,
            borderWidth: 2,
            pointRadius: (context) => singleDayPointRadius(context, 4),
            pointHoverRadius: (context) =>
              isSingleDay ? singleDayPointRadius(context, 5) : 4,
            pointHitRadius: 12,
            pointBorderColor: "#242424",
            pointBorderWidth: isSingleDay ? 2 : 0,
            tension: 0.3,
          }
        : {
            label: item.name,
            data: item.values,
            backgroundColor: item.color,
            hoverBackgroundColor: item.color,
            borderWidth: 0,
            borderRadius: 2,
            maxBarThickness: 18,
          },
    ),
  };
});

// 柱图 hover 时铺满整列；折线和饼图不使用该视觉语义。
const columnHighlightPlugin = {
  id: "usageColumnHighlight",
  beforeDatasetsDraw(chart) {
    if (props.chartType !== "bar") return;
    const active = chart.tooltip?.getActiveElements?.() ?? [];
    if (active.length === 0) return;
    const { ctx, chartArea, scales } = chart;
    const center = scales.x?.getPixelForValue(active[0].index);
    if (!Number.isFinite(center)) return;
    const count = Math.max(1, chart.data.labels.length);
    const width = Math.min(28, (chartArea.right - chartArea.left) / count);
    ctx.save();
    ctx.fillStyle = "rgba(255, 255, 255, 0.06)";
    ctx.fillRect(center - width / 2, chartArea.top, width, chartArea.bottom - chartArea.top);
    ctx.restore();
  },
};

const commonTooltip = computed(() => ({
  enabled: false,
  external: externalTooltip.handler,
  // caretX/caretY 在 Chart.js 里属于被动画补间的数值属性，external handler 每帧都会
  // 收到插值中的中间坐标；元素首次创建时它们是 undefined，于是从 (0,0) 起算 ——
  // 这才是浮层「从左上角飘过来」的源头，CSS 层改不掉。关掉后拿到的直接是目标坐标。
  animation: false,
  displayColors: false,
  callbacks:
    props.chartType === "pie"
      ? {
          title: () => "区间占比",
          label: (context) => {
            const value = asUsageNumber(context.raw);
            const total = context.dataset.data.reduce((sum, item) => sum + asUsageNumber(item), 0);
            const share = total > 0 ? `${((value / total) * 100).toFixed(1)}%` : "0%";
            return `${context.label}：${formatDetailedValue(value)}（${share}）`;
          },
        }
      : {
          title: (items) => tooltipTitle(items[0]?.label),
          beforeBody: (items) => {
            const total = items.reduce((sum, item) => sum + asUsageNumber(item.parsed.y), 0);
            return `${METRIC_LABELS[props.metric] ?? "合计"}：${formatDetailedValue(total)}`;
          },
          label: (context) =>
            `${context.dataset.label}：${formatDetailedValue(context.parsed.y)}`,
        },
}));

const chartOptions = computed(() => {
  const base = {
    responsive: true,
    maintainAspectRatio: false,
    animation: { duration: 220 },
    layout: { padding: props.chartType === "pie" ? 6 : { top: 4 } },
    plugins: {
      legend: { display: false },
      tooltip: commonTooltip.value,
    },
  };
  if (props.chartType === "pie") {
    return base;
  }
  const stacked = props.chartType === "bar";
  return {
    ...base,
    interaction: { mode: "index", intersect: false },
    scales: {
      x: {
        stacked,
        grid: { display: false },
        border: { display: false },
        ticks: {
          color: "#8f8f8f",
          font: { size: 11 },
          maxRotation: 0,
          autoSkipPadding: 10,
          callback(value) {
            return axisLabel(this.getLabelForValue(value));
          },
        },
      },
      y: {
        stacked,
        beginAtZero: true,
        display: !props.compact,
        grid: { color: "#2c2c2c" },
        border: { display: false },
        ticks: {
          color: "#8f8f8f",
          font: { size: 10 },
          callback: (value) => formatUsageValue(value, props.metric),
        },
      },
    },
  };
});

const legendItems = computed(() =>
  props.chartType === "pie" ? shareSeries.value : series.value,
);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-2">
    <div
      v-if="!hasData"
      class="flex h-full min-h-0 flex-1 flex-col items-center justify-center gap-1 rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] text-center"
    >
      <span class="icon-[mdi--chart-bar] text-[20px] text-[#5a5a5a]"></span>
      <span class="text-sm text-[#a3a3a3]">暂无调用数据</span>
      <span v-if="!compact" class="max-w-[560px] text-xs text-[#6f6f6f]">
        发起对话后这里会显示每日用量；按中转站与按模型的分层只对新产生的记录生效
      </span>
    </div>
    <template v-else>
      <!-- key 只绑 chartKind：Chart.js 的 type 不可变更，换形态必须重建实例。
           而 metric / dimension 变化时保持同一实例，才能吃到它自带的数据插值动画。 -->
      <div class="relative min-h-0 flex-1">
        <Transition name="mo-chart">
          <Chart
            :key="chartKind"
            class="h-full w-full"
            :type="chartKind"
            :data="chartData"
            :options="chartOptions"
            :plugins="[columnHighlightPlugin]"
          />
        </Transition>
      </div>
      <div v-if="!compact" class="center-row flex-wrap gap-x-3 gap-y-1 text-xs text-[#a3a3a3]">
        <span v-for="item in legendItems" :key="item.id" class="center-row gap-1.5">
          <span class="h-2 w-2 rounded-[2px]" :style="{ backgroundColor: item.color }"></span>
          <span>{{ item.name }}</span>
        </span>
      </div>
    </template>
  </div>
</template>