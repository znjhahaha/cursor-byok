<script setup>
import { autoUpdate, computePosition, flip, offset, shift, size } from "@floating-ui/dom";
import { computed, nextTick, onBeforeUnmount, ref, watch, watchPostEffect } from "vue";

// 多选过滤组件：空数组语义为「不筛选（全部）」，与单选 Select 的占位语义区分。
const props = defineProps({
  modelValue: { type: Array, default: () => [] },
  options: { type: Array, default: () => [] },
  placeholder: { type: String, default: "全部" },
  disabled: { type: Boolean, default: false },
  ariaLabel: { type: String, default: "" },
});

const emit = defineEmits(["update:modelValue"]);

const rootRef = ref(null);
const buttonRef = ref(null);
const menuRef = ref(null);
const searchRef = ref(null);
const isOpen = ref(false);
const keyword = ref("");
const menuStyle = ref({});

const normalizedOptions = computed(() =>
  props.options.map((option) => ({
    label: String(option?.label ?? option?.value ?? ""),
    value: String(option?.value ?? ""),
    hint: String(option?.hint ?? ""),
  })),
);

const selectedSet = computed(() => new Set(props.modelValue.map((item) => String(item))));

const filteredOptions = computed(() => {
  const text = keyword.value.trim().toLowerCase();
  if (!text) {
    return normalizedOptions.value;
  }
  return normalizedOptions.value.filter(
    (option) =>
      option.label.toLowerCase().includes(text) || option.hint.toLowerCase().includes(text),
  );
});

const triggerLabel = computed(() => {
  const count = selectedSet.value.size;
  return count > 0 ? `${props.placeholder} · ${count}` : props.placeholder;
});

function openMenu() {
  if (props.disabled || isOpen.value) {
    return;
  }
  isOpen.value = true;
  keyword.value = "";
  nextTick(() => {
    updatePosition();
    searchRef.value?.focus();
  });
}

function closeMenu({ restoreFocus = false } = {}) {
  if (!isOpen.value) {
    return;
  }
  isOpen.value = false;
  keyword.value = "";
  menuStyle.value = {};
  if (restoreFocus) {
    nextTick(() => buttonRef.value?.focus());
  }
}

function toggleMenu() {
  if (isOpen.value) {
    closeMenu();
    return;
  }
  openMenu();
}

function toggleOption(value) {
  const next = new Set(selectedSet.value);
  if (next.has(value)) {
    next.delete(value);
  } else {
    next.add(value);
  }
  emit("update:modelValue", Array.from(next));
}

function selectAllFiltered() {
  const next = new Set(selectedSet.value);
  for (const option of filteredOptions.value) {
    next.add(option.value);
  }
  emit("update:modelValue", Array.from(next));
}

function clearAll() {
  emit("update:modelValue", []);
}

function handleButtonKeydown(event) {
  if (props.disabled) {
    return;
  }
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    toggleMenu();
    return;
  }
  if (event.key === "Escape" && isOpen.value) {
    event.preventDefault();
    closeMenu();
  }
}

function handleMenuKeydown(event) {
  if (event.key === "Escape") {
    event.preventDefault();
    closeMenu({ restoreFocus: true });
  }
}

function handlePointerDown(event) {
  if (rootRef.value?.contains(event.target) || menuRef.value?.contains(event.target)) {
    return;
  }
  closeMenu();
}

function updatePosition() {
  if (!buttonRef.value || !menuRef.value) {
    return;
  }
  computePosition(buttonRef.value, menuRef.value, {
    placement: "bottom-start",
    middleware: [
      offset(8),
      flip({ padding: 12 }),
      shift({ padding: 12 }),
      size({
        apply({ elements, availableHeight }) {
          Object.assign(elements.floating.style, {
            maxHeight: `${Math.max(160, Math.min(availableHeight, 360))}px`,
          });
        },
        padding: 12,
      }),
    ],
  }).then(({ x, y, placement }) => {
    menuStyle.value = {
      left: `${x}px`,
      top: `${y}px`,
      transformOrigin: placement.startsWith("top") ? "bottom" : "top",
    };
  });
}

watchPostEffect((cleanup) => {
  if (!isOpen.value || !buttonRef.value || !menuRef.value) {
    return;
  }
  const stopAutoUpdate = autoUpdate(buttonRef.value, menuRef.value, updatePosition);
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
      class="flex h-9 w-auto items-center justify-start gap-2 rounded-[6px] border bg-[#232323] px-3 text-left text-sm outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-60"
      :class="[
        selectedSet.size > 0
          ? 'border-[#1ca35a]/60 text-[#86efac]'
          : 'border-transparent text-[#7b7b7b]',
        'focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35',
      ]"
      :aria-expanded="isOpen"
      :aria-label="ariaLabel || placeholder"
      aria-haspopup="listbox"
      @click="toggleMenu"
      @keydown="handleButtonKeydown"
    >
      <span class="truncate">{{ triggerLabel }}</span>
      <span
        v-if="selectedSet.size > 0"
        class="center-row shrink-0 text-current"
        title="清空筛选"
        @click.stop="clearAll"
      >
        <span class="icon-[ic--round-close] text-[14px]"></span>
      </span>
      <span
        class="pointer-events-none center-row shrink-0 transition-transform duration-200"
        :class="isOpen ? 'rotate-180' : ''"
      >
        <span class="icon-[mdi--chevron-down] text-[18px]"></span>
      </span>
    </button>
  </div>

  <Teleport to="body">
    <Transition name="mo-pop">
      <div
        v-if="isOpen"
        ref="menuRef"
        class="fixed z-[999] flex w-[240px] flex-col overflow-hidden rounded-[8px] border border-[#3f3f3f] bg-[#232323] shadow-[0_16px_30px_-12px_rgba(0,0,0,0.7)]"
        :style="menuStyle"
        @keydown="handleMenuKeydown"
      >
        <div class="shrink-0 border-b border-[#303030] p-2">
          <input
            ref="searchRef"
            v-model="keyword"
            type="text"
            class="h-7 w-full rounded-[6px] border border-[#3f3f3f] bg-[#1d1d1d] px-2 text-xs text-[#e5e5e5] outline-none placeholder:text-[#666] focus:border-[#10AD5D]"
            placeholder="搜索..."
          />
        </div>
        <div class="center-row shrink-0 justify-between border-b border-[#303030] px-2 py-1">
          <button
            type="button"
            class="rounded px-1 py-0.5 text-[11px] text-[#8f8f8f] transition-colors hover:text-[#d4d4d4]"
            @click="selectAllFiltered"
          >
            全选
          </button>
          <button
            type="button"
            class="rounded px-1 py-0.5 text-[11px] text-[#8f8f8f] transition-colors hover:text-[#d4d4d4]"
            @click="clearAll"
          >
            清空（不筛选）
          </button>
        </div>
        <ul role="listbox" aria-multiselectable="true" class="min-h-0 flex-1 overflow-y-auto p-1.5">
          <li v-if="filteredOptions.length === 0" class="px-3 py-2 text-xs text-[#737373]">
            无匹配项
          </li>
          <li v-for="option in filteredOptions" :key="option.value">
            <button
              type="button"
              role="option"
              class="flex w-full items-center gap-2 rounded-[6px] px-2 py-1.5 text-left text-sm outline-none transition-colors"
              :class="selectedSet.has(option.value) ? 'text-[#10d06f]' : 'text-[#e5e5e5] hover:bg-[#303030]'"
              :aria-selected="selectedSet.has(option.value)"
              @click="toggleOption(option.value)"
            >
              <span
                class="center-row h-4 w-4 shrink-0 rounded-[4px] border"
                :class="selectedSet.has(option.value) ? 'border-[#10AD5D] bg-[#10AD5D]/20' : 'border-[#4a4a4a]'"
              >
                <span v-if="selectedSet.has(option.value)" class="icon-[mdi--check] text-[12px]"></span>
              </span>
              <span class="min-w-0 flex-1 truncate" :title="option.label">{{ option.label }}</span>
              <span v-if="option.hint" class="shrink-0 text-[11px] text-[#737373]">{{ option.hint }}</span>
            </button>
          </li>
        </ul>
      </div>
    </Transition>
  </Teleport>
</template>