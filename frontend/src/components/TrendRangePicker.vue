<script setup>
// 趋势统计区间的选择面板：预设天数点即应用；自定义用 slider 拖动、松手应用。
// 后端 LoadUsageSeries 只接受「回溯 N 个自然日（含今天）」，所以这里选的是天数而非日历起止。
import { autoUpdate, computePosition, flip, offset, shift } from "@floating-ui/dom";
import { computed, nextTick, onBeforeUnmount, ref, watch, watchPostEffect } from "vue";

const PRESETS = [1, 7, 14, 30, 90];

const props = defineProps({
  range: { type: String, default: "14" },
  customDays: { type: Number, default: 21 },
  min: { type: Number, default: 1 },
  max: { type: Number, default: 365 },
  disabled: { type: Boolean, default: false },
  ariaLabel: { type: String, default: "统计区间" },
});

const emit = defineEmits(["select", "apply"]);

const rootRef = ref(null);
const buttonRef = ref(null);
const panelRef = ref(null);
const isOpen = ref(false);
const panelStyle = ref({});
const draftDays = ref(props.customDays);

const triggerLabel = computed(() =>
  props.range === "custom" ? `自定义 ${props.customDays} 天` : `${props.range} 天`,
);

const sliderProgress = computed(() => {
  const min = Number(props.min);
  const max = Number(props.max);
  if (!Number.isFinite(min) || !Number.isFinite(max) || max <= min) {
    return "0%";
  }
  return `${((clampDays(draftDays.value) - min) / (max - min)) * 100}%`;
});

function clampDays(value) {
  const parsed = Math.round(Number(value));
  if (!Number.isFinite(parsed)) {
    return props.customDays;
  }
  return Math.min(props.max, Math.max(props.min, parsed));
}

function effectiveDays() {
  return props.range === "custom" ? clampDays(props.customDays) : clampDays(props.range);
}

function openPanel() {
  if (props.disabled || isOpen.value) {
    return;
  }
  // 每次打开把草稿对齐到当前生效区间，避免上次未应用的残留值。
  draftDays.value = effectiveDays();
  isOpen.value = true;
  nextTick(updatePosition);
}

function closePanel({ restoreFocus = false } = {}) {
  if (!isOpen.value) {
    return;
  }
  isOpen.value = false;
  panelStyle.value = {};
  if (restoreFocus) {
    nextTick(() => buttonRef.value?.focus());
  }
}

function togglePanel() {
  if (isOpen.value) {
    closePanel({ restoreFocus: true });
    return;
  }
  openPanel();
}

function choosePreset(days) {
  emit("select", String(days));
  closePanel({ restoreFocus: true });
}

// slider 松手（change）即应用，但面板不收起：图表在背景里即时刷新，
// 用户可以继续微调；点外部、Esc 或选预设才关闭。键盘方向键也因此可用。
function applyDraft() {
  emit("apply", clampDays(draftDays.value));
}

function handleSliderInput(event) {
  draftDays.value = clampDays(event.target.value);
}

function handleButtonKeydown(event) {
  if (props.disabled) {
    return;
  }
  switch (event.key) {
    case "Enter":
    case " ":
    case "ArrowDown":
      event.preventDefault();
      togglePanel();
      break;
    case "Escape":
      if (isOpen.value) {
        event.preventDefault();
        closePanel({ restoreFocus: true });
      }
      break;
    default:
      break;
  }
}

function handlePointerDown(event) {
  if (rootRef.value?.contains(event.target) || panelRef.value?.contains(event.target)) {
    return;
  }
  closePanel();
}

function updatePosition() {
  if (!buttonRef.value || !panelRef.value) {
    return;
  }
  computePosition(buttonRef.value, panelRef.value, {
    placement: "bottom-start",
    middleware: [offset(6), flip({ padding: 12 }), shift({ padding: 12 })],
  }).then(({ x, y, placement }) => {
    panelStyle.value = {
      left: `${x}px`,
      top: `${y}px`,
      transformOrigin: placement.startsWith("top") ? "bottom" : "top",
    };
  });
}

watchPostEffect((cleanup) => {
  if (!isOpen.value || !buttonRef.value || !panelRef.value) {
    return;
  }
  const stopAutoUpdate = autoUpdate(buttonRef.value, panelRef.value, updatePosition);
  cleanup(() => {
    stopAutoUpdate();
  });
});

watch(isOpen, (open) => {
  if (open) {
    document.addEventListener("pointerdown", handlePointerDown);
    return;
  }
  document.removeEventListener("pointerdown", handlePointerDown);
});

onBeforeUnmount(() => {
  document.removeEventListener("pointerdown", handlePointerDown);
});
</script>

<template>
  <div ref="rootRef" class="relative">
    <button
      ref="buttonRef"
      type="button"
      :disabled="disabled"
      class="center-row h-[24px] min-h-0 gap-1 rounded-[6px] border px-2 text-[11px] outline-none transition-[color,background-color,border-color,box-shadow] duration-150 focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35 disabled:cursor-not-allowed disabled:opacity-60"
      :class="isOpen
        ? 'border-[#4c4c4c] bg-[#2d2d2d] text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]'
        : 'border-transparent text-[#a3a3a3] hover:border-[#3b3b3b] hover:bg-[#282828] hover:text-[#e5e5e5]'"
      :aria-expanded="isOpen"
      :aria-label="ariaLabel"
      aria-haspopup="dialog"
      @click="togglePanel"
      @keydown="handleButtonKeydown"
    >
      <span class="truncate">{{ triggerLabel }}</span>
      <span
        class="center-row transition-transform duration-200"
        :class="{ 'rotate-180': isOpen }"
      >
        <span class="icon-[mdi--chevron-down] text-[14px]"></span>
      </span>
    </button>
  </div>

  <Teleport to="body">
    <Transition name="mo-pop">
      <div
        v-if="isOpen"
        ref="panelRef"
        class="fixed z-[999] w-[276px] rounded-[10px] border border-[#414141] bg-[#232323] p-3 shadow-[0_18px_42px_-14px_rgba(0,0,0,0.8)]"
        :style="panelStyle"
        role="dialog"
        :aria-label="ariaLabel"
        @keydown.esc.prevent="closePanel({ restoreFocus: true })"
      >
        <div class="center-row mb-2.5 justify-between px-0.5">
          <span class="text-[11px] font-medium text-[#a3a3a3]">统计区间</span>
          <span class="text-[10px] text-[#686868]">{{ triggerLabel }}</span>
        </div>

        <div class="grid grid-cols-5 gap-1.5">
          <button
            v-for="days in PRESETS"
            :key="days"
            type="button"
            class="center-row h-[30px] justify-center rounded-[6px] border text-[11px] transition-[color,background-color,border-color,box-shadow] duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35"
            :class="range === String(days)
              ? 'border-[#4b765f] bg-[#294437] text-[#d8f3e3] shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]'
              : 'border-[#353535] bg-[#272727] text-[#8a8a8a] hover:border-[#474747] hover:bg-[#2d2d2d] hover:text-[#d4d4d4]'"
            :aria-pressed="range === String(days)"
            @click="choosePreset(days)"
          >
            {{ days }}天
          </button>
        </div>

        <div class="mt-3 rounded-[8px] border border-[#363636] bg-[#202020] p-2.5">
          <div class="center-row justify-between">
            <div>
              <div class="text-[11px] font-medium text-[#a3a3a3]">自定义天数</div>
              <div class="mt-0.5 text-[10px] text-[#606060]">拖动滑块，松开后应用</div>
            </div>
            <span
              class="min-w-[52px] rounded-[6px] border border-[#3d5f4d] bg-[#26382f] px-2 py-1 text-center text-[12px] text-[#d8f3e3]"
              style="font-family: var(--font-num)"
            >
              {{ draftDays }} 天
            </span>
          </div>

          <input
            type="range"
            :min="min"
            :max="max"
            :value="draftDays"
            class="trend-range-slider mt-3 w-full"
            :style="{ '--range-progress': sliderProgress }"
            :aria-label="`自定义天数，${min} 到 ${max} 天`"
            @input="handleSliderInput"
            @change="applyDraft"
          />

          <div class="center-row mt-1.5 justify-between text-[10px] text-[#5f5f5f]">
            <span>{{ min }} 天</span>
            <span>{{ max }} 天</span>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.trend-range-slider {
  height: 18px;
  cursor: pointer;
  appearance: none;
  background: transparent;
  outline: none;
}

.trend-range-slider::-webkit-slider-runnable-track {
  height: 4px;
  border-radius: 999px;
  background: linear-gradient(
    to right,
    #55b57e 0,
    #55b57e var(--range-progress),
    #3a3a3a var(--range-progress),
    #3a3a3a 100%
  );
}

.trend-range-slider::-webkit-slider-thumb {
  width: 16px;
  height: 16px;
  margin-top: -6px;
  appearance: none;
  border: 2px solid #72c995;
  border-radius: 999px;
  background: #326b4b;
  box-shadow: 0 0 0 3px #202020, 0 2px 6px rgba(0, 0, 0, 0.45);
  transition: background-color 150ms ease, border-color 150ms ease, transform 150ms ease;
}

.trend-range-slider:hover::-webkit-slider-thumb {
  border-color: #8bdaa9;
  background: #3e805a;
  transform: scale(1.06);
}

.trend-range-slider:focus-visible::-webkit-slider-thumb {
  box-shadow: 0 0 0 3px #202020, 0 0 0 5px rgba(16, 173, 93, 0.32);
}

.trend-range-slider::-moz-range-track {
  height: 4px;
  border-radius: 999px;
  background: #3a3a3a;
}

.trend-range-slider::-moz-range-progress {
  height: 4px;
  border-radius: 999px;
  background: #55b57e;
}

.trend-range-slider::-moz-range-thumb {
  width: 14px;
  height: 14px;
  border: 2px solid #72c995;
  border-radius: 999px;
  background: #326b4b;
  box-shadow: 0 0 0 3px #202020, 0 2px 6px rgba(0, 0, 0, 0.45);
  transition: background-color 150ms ease, border-color 150ms ease, transform 150ms ease;
}

.trend-range-slider:hover::-moz-range-thumb {
  border-color: #8bdaa9;
  background: #3e805a;
  transform: scale(1.06);
}

.trend-range-slider:focus-visible::-moz-range-thumb {
  box-shadow: 0 0 0 3px #202020, 0 0 0 5px rgba(16, 173, 93, 0.32);
}
</style>