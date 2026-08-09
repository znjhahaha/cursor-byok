<script setup>
import { computed } from "vue";
import Spinner from "@/components/ui/Spinner.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { formatDuration } from "@/state/appState";

const props = defineProps({
  result: {
    type: Object,
    default: null,
  },
  stale: {
    type: Boolean,
    default: false,
  },
  compact: {
    type: Boolean,
    default: false,
  },
  showMetrics: {
    type: Boolean,
    default: false,
  },
  title: {
    type: String,
    default: "模型测试",
  },
  emptyText: {
    type: String,
    default: "尚未测试",
  },
});

const emit = defineEmits(["cancel"]);

const normalizedStatus = computed(() => {
  const status = String(props.result?.status || "").trim().toLowerCase();
  return ["running", "success", "error", "canceled"].includes(status) ? status : "idle";
});

// 预热排队是 running 的一个子态：请求已发出但上游让我们等号，
// 与普通「测试中」区分开，用户才知道这次久等是排队而不是卡死。
const warmupWaiting = computed(
  () => normalizedStatus.value === "running" && Boolean(props.result?.warmupWaiting),
);

const warmupElapsed = computed(() => formatDuration(props.result?.warmupElapsedMS));
const warmupNextRetry = computed(() => formatDuration(props.result?.warmupNextRetryMS));

const summaryText = computed(() => {
  const text = String(props.result?.summaryText || "").trim();
  if (text) {
    return text;
  }
  if (normalizedStatus.value === "running") {
    return "测试中...";
  }
  if (normalizedStatus.value === "error") {
    return "测试失败";
  }
  if (normalizedStatus.value === "canceled") {
    return "排队检测已取消";
  }
  return props.emptyText;
});

const rawResponseText = computed(() => {
  const raw = String(props.result?.rawResponse || "").trim();
  if (raw) {
    return raw;
  }
  if (normalizedStatus.value === "error" || normalizedStatus.value === "canceled") {
    return String(props.result?.error || "").trim();
  }
  return "";
});

const panelClass = computed(() => {
  if (props.stale) {
    return "border-[#6b5b1e] bg-[#2c2612]";
  }
  if (normalizedStatus.value === "running") {
    return warmupWaiting.value ? "border-[#5a4314] bg-[#2f2612]" : "border-[#164e63] bg-[#0b2530]";
  }
  if (normalizedStatus.value === "error" || normalizedStatus.value === "canceled") {
    return "border-[#4b1d1d] bg-[#2a1313]";
  }
  if (normalizedStatus.value === "success" && (!props.result?.benchmarkComplete || props.result?.tokensEstimated)) {
    return "border-[#5a4314] bg-[#2f2612]";
  }
  if (normalizedStatus.value === "success") {
    return "border-[#14532d] bg-[#102418]";
  }
  return "border-[#343434] bg-[#232323]";
});

const summaryClass = computed(() => {
  if (props.stale) {
    return "text-[#f6d77a]";
  }
  if (normalizedStatus.value === "running") {
    return warmupWaiting.value ? "text-[#fcd34d]" : "text-[#67e8f9]";
  }
  if (normalizedStatus.value === "error") {
    return "text-[#fca5a5]";
  }
  if (normalizedStatus.value === "success" && (!props.result?.benchmarkComplete || props.result?.tokensEstimated)) {
    return "text-[#fcd34d]";
  }
  if (normalizedStatus.value === "success") {
    return "text-[#86efac]";
  }
  return "text-[#a3a3a3]";
});
</script>

<template>
  <!-- panelClass / summaryClass 会在 idle→running→success/error 之间换掉整块的
       边框、背景与文字色。批量测速是 10 并发，缺了 transition 就是一片色块乱闪。 -->
  <div
    class="rounded-[8px] border px-3 py-3 transition-colors duration-enter"
    :class="panelClass"
    :aria-busy="normalizedStatus === 'running' || undefined"
  >
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-1.5">
          <Spinner
            v-if="normalizedStatus === 'running'"
            :class="warmupWaiting ? 'text-[#fcd34d]' : 'text-[#67e8f9]'"
          />
          <div
            :class="compact ? 'text-[11px] uppercase tracking-[0.08em] text-[#666]' : 'text-sm font-medium text-white'"
          >
            {{ title }}
          </div>
          <div v-if="rawResponseText" class="center-row gap-1 text-[11px] text-[#8f8f8f]">
            <span>原始返回</span>
            <Tooltip :content="rawResponseText" copyable />
          </div>
        </div>
        <div class="mt-1 text-sm leading-relaxed" :class="summaryClass">
          {{ summaryText }}
        </div>
      </div>
      <!-- 次数与时长都已由下方 summary 行和定宽格子承载，
           徽章只做状态标识、保持恒定宽度，避免它随重试次数增位去挤压左侧标题。 -->
      <span
        v-if="warmupWaiting"
        class="shrink-0 rounded-[999px] border border-[#8a6d1a] px-2 py-1 text-xs text-[#f6d77a]"
      >
        排队预热
      </span>
      <span
        v-else-if="stale"
        class="shrink-0 rounded-[999px] border border-[#8a6d1a] px-2 py-1 text-xs text-[#f6d77a]"
      >
        需重测
      </span>
    </div>

    <!-- 这两个读数每 500ms 重写一次，位数还会在 "999 ms" 与 "123.4 s" 之间变。
         原先它们和取消按钮同处一个 flex-wrap 行：文本一变长就把按钮挤到第二行，
         下一跳变短又收回来，表现为卡片宽高反复横跳。
         改成定宽两列独立承载，数字再长也只在各自格子内变化，外层尺寸恒定；
         tabular-nums 进一步锁掉等宽数字之间的字形宽度差。 -->
    <div v-if="warmupWaiting" class="mt-2 flex flex-col gap-2">
      <div class="grid grid-cols-2 gap-2">
        <div class="min-w-0 rounded-[6px] bg-[#241d0c] px-2.5 py-1.5">
          <div class="text-[10px] uppercase tracking-[0.08em] text-[#9a8340]">已等待</div>
          <div class="mt-0.5 truncate text-xs tabular-nums text-[#f6d77a]">{{ warmupElapsed }}</div>
        </div>
        <div class="min-w-0 rounded-[6px] bg-[#241d0c] px-2.5 py-1.5">
          <div class="text-[10px] uppercase tracking-[0.08em] text-[#9a8340]">下次尝试</div>
          <div class="mt-0.5 truncate text-xs tabular-nums text-[#f6d77a]">{{ warmupNextRetry }}</div>
        </div>
      </div>
      <button
        v-if="result?.warmupCancelable"
        type="button"
        class="w-full rounded-[6px] border border-[#8a6d1a] px-2 py-1 text-xs text-[#f6d77a] transition-colors hover:bg-[#3a2d12]"
        @click="emit('cancel')"
      >
        取消排队
      </button>
    </div>

    <div v-if="stale" class="mt-2 text-xs text-[#f6d77a]">
      配置已变更，请重新测试
    </div>

    <div
      v-if="showMetrics && normalizedStatus === 'success'"
      class="mt-3 grid grid-cols-1 gap-2 md:grid-cols-2"
    >
      <div class="rounded-[8px] bg-[#1c1c1c] px-3 py-2">
        <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">总耗时</div>
        <div class="mt-1 text-sm tabular-nums text-[#d4d4d4]">{{ formatDuration(result?.totalDurationMS) }}</div>
      </div>
      <div class="rounded-[8px] bg-[#1c1c1c] px-3 py-2">
        <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">输出 Token</div>
        <div class="mt-1 text-sm tabular-nums text-[#d4d4d4]">{{ result?.outputTokens ?? 0 }}</div>
      </div>
    </div>

    <div
      v-if="normalizedStatus === 'success' && result?.tokensEstimated"
      class="mt-2 text-xs text-[#8f8f8f]"
    >
      输出 Token 为估算值
    </div>
    <div
      v-if="normalizedStatus === 'success' && result?.warning"
      class="mt-2 rounded-[6px] border border-[#5a4314] bg-[#221b0d] px-2 py-1.5 text-xs text-[#fcd34d]"
    >
      {{ result.warning }}
    </div>
  </div>
</template>
