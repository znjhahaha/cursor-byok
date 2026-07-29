<script setup>
import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";
import { computed } from "vue";

const props = defineProps({
  hours: { type: Array, default: () => [] },
});

const points = computed(() => {
  const source = new Map(
    props.hours.map((item) => [Number(item?.hour), item]),
  );
  return Array.from({ length: 24 }, (_, hour) => {
    const raw = source.get(hour) ?? {};
    return {
      hour,
      providerCalls: Number(raw.providerCalls ?? 0),
      totalTokens: Number(raw.totalTokens ?? 0),
    };
  });
});

const maxCalls = computed(() =>
  Math.max(0, ...points.value.map((item) => item.providerCalls)),
);
const totalCalls = computed(() =>
  points.value.reduce((sum, item) => sum + item.providerCalls, 0),
);

function barHeight(value) {
  if (value <= 0 || maxCalls.value <= 0) {
    return "0%";
  }
  return `${Math.max(6, (value / maxCalls.value) * 100)}%`;
}

function hourLabel(hour) {
  return `${String(hour).padStart(2, "0")}:00`;
}

function tooltip(item) {
  return `${hourLabel(item.hour)} · ${formatInteger(item.providerCalls)} 次调用 · ${formatInteger(item.totalTokens)} Token`;
}
</script>

<template>
  <div class="flex h-full min-h-[150px] flex-col">
    <div v-if="totalCalls === 0" class="flex flex-1 flex-col items-center justify-center gap-1 text-center">
      <span class="icon-[mdi--clock-outline] text-[20px] text-[#5a5a5a]"></span>
      <span class="text-sm text-[#a3a3a3]">暂无小时分布数据</span>
      <span class="text-xs text-[#6f6f6f]">该统计从本版本开始累积</span>
    </div>
    <template v-else>
      <div class="flex min-h-0 flex-1 items-end gap-1 border-b border-[#343434] px-1 pt-3">
        <div
          v-for="item in points"
          :key="item.hour"
          class="group relative flex h-full min-w-0 flex-1 items-end justify-center"
          :title="tooltip(item)"
        >
          <div
            class="w-full max-w-[16px] rounded-t-[2px] bg-[#1ca35a] transition-[height,background-color] duration-200 group-hover:bg-[#38c172]"
            :style="{ height: barHeight(item.providerCalls) }"
          ></div>
          <div
            class="pointer-events-none absolute bottom-[calc(100%+6px)] left-1/2 z-10 hidden -translate-x-1/2 whitespace-nowrap rounded-[6px] border border-[#424242] bg-[#1d1d1d] px-2 py-1 text-[11px] text-[#d4d4d4] shadow-lg group-hover:block"
          >
            <div>{{ hourLabel(item.hour) }} · {{ formatInteger(item.providerCalls) }} 次</div>
            <div class="text-[#8f8f8f]">{{ formatCompactInteger(item.totalTokens) }} Token</div>
          </div>
        </div>
      </div>
      <div class="flex justify-between px-1 pt-1 text-[10px] text-[#737373]">
        <span>00:00</span>
        <span>06:00</span>
        <span>12:00</span>
        <span>18:00</span>
        <span>23:00</span>
      </div>
    </template>
  </div>
</template>