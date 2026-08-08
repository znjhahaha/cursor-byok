<script setup>
import { autoUpdate, computePosition, flip, offset, shift, size } from "@floating-ui/dom";
import { computed, nextTick, onBeforeUnmount, ref, useId, watch, watchPostEffect } from "vue";

// Combobox 与 Select 的分工：Select 的值必须落在选项里，选项是全部合法值；
// Combobox 的值由用户自由输入，选项只是候选提示。所以这里的过滤基准是
// modelValue 本身，而不是另存一份 query —— 多存一份就会出现「输入框显示 A、
// 下拉按 B 过滤」的两个真值。
const props = defineProps({
  modelValue: { type: String, default: "" },
  options: { type: Array, default: () => [] },
  placeholder: { type: String, default: "" },
  emptyText: { type: String, default: "没有匹配项" },
  loading: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
  ariaLabel: { type: String, default: "" },
});

const emit = defineEmits(["update:modelValue", "change", "blur"]);

const rootRef = ref(null);
const inputRef = ref(null);
const menuRef = ref(null);
const optionRefs = ref([]);
const isOpen = ref(false);
const activeIndex = ref(-1);
// showAllOptions 区分「点开箭头看全部」与「边打字边收窄」：
// 前者不该被已填入的值过滤掉，否则选中一项后再点开只剩它自己。
const showAllOptions = ref(false);
const menuStyle = ref({});
const listboxID = useId();

const normalizedOptions = computed(() => props.options.map((option, optionIndex) => {
  if (typeof option === "string") {
    return { label: option, value: option, icon: "", optionIndex };
  }

  return {
    label: option?.label ?? option?.value ?? "",
    value: option?.value ?? "",
    icon: option?.icon ?? option?.iconClass ?? "",
    optionIndex,
  };
}));

const filteredOptions = computed(() => {
  const query = String(props.modelValue || "").trim().toLocaleLowerCase();
  if (showAllOptions.value || !query) {
    return normalizedOptions.value;
  }

  return normalizedOptions.value.filter((option) => (
    String(option.label).toLocaleLowerCase().includes(query)
    || String(option.value).toLocaleLowerCase().includes(query)
  ));
});
const activeOption = computed(() => filteredOptions.value[activeIndex.value] ?? null);
const activeDescendant = computed(() => (
  isOpen.value && activeOption.value ? `${listboxID}-option-${activeOption.value.optionIndex}` : undefined
));

function setOptionRef(el, index) {
  if (el) {
    optionRefs.value[index] = el;
    return;
  }

  delete optionRefs.value[index];
}

// 键盘移动只滚动、不抢焦点：焦点必须留在输入框，否则继续打字会丢字符。
function scrollActiveOptionIntoView() {
  nextTick(() => optionRefs.value[activeOption.value?.optionIndex]?.scrollIntoView({ block: "nearest" }));
}

function openMenu({ showAll = false } = {}) {
  if (props.disabled || normalizedOptions.value.length === 0) {
    return;
  }

  showAllOptions.value = showAll;
  isOpen.value = true;
  const selectedIndex = filteredOptions.value.findIndex((option) => option.value === props.modelValue);
  activeIndex.value = selectedIndex >= 0 ? selectedIndex : filteredOptions.value.length ? 0 : -1;
  nextTick(updatePosition);
}

function closeMenu({ restoreFocus = false, notifyBlur = false } = {}) {
  if (isOpen.value) {
    isOpen.value = false;
    activeIndex.value = -1;
    optionRefs.value = [];
    menuStyle.value = {};
  }

  if (restoreFocus) {
    nextTick(() => inputRef.value?.focus());
  }
  if (notifyBlur) {
    emit("blur");
  }
}

function handleInput(event) {
  emit("update:modelValue", event.target.value);
  showAllOptions.value = false;
  isOpen.value = normalizedOptions.value.length > 0;
  activeIndex.value = filteredOptions.value.length ? 0 : -1;
}

function selectOption(option) {
  if (!option) {
    return;
  }

  emit("update:modelValue", option.value);
  emit("change", option.value);
  closeMenu();
}

function toggleMenu() {
  if (isOpen.value) {
    closeMenu({ restoreFocus: true });
    return;
  }

  openMenu({ showAll: true });
  nextTick(() => inputRef.value?.focus());
}

function moveActiveIndex(step) {
  if (!isOpen.value) {
    openMenu({ showAll: true });
    return;
  }

  const total = filteredOptions.value.length;
  if (!total) {
    return;
  }

  const current = activeIndex.value >= 0 ? activeIndex.value : 0;
  activeIndex.value = (current + step + total) % total;
  scrollActiveOptionIntoView();
}

function handleInputKeydown(event) {
  switch (event.key) {
    case "ArrowDown":
      event.preventDefault();
      moveActiveIndex(1);
      break;
    case "ArrowUp":
      event.preventDefault();
      moveActiveIndex(-1);
      break;
    case "Enter":
      // 只在高亮了候选时拦截回车，否则要把回车留给外层表单提交。
      if (isOpen.value && activeIndex.value >= 0) {
        event.preventDefault();
        selectOption(filteredOptions.value[activeIndex.value]);
      }
      break;
    case "Escape":
      if (isOpen.value) {
        event.preventDefault();
        closeMenu({ restoreFocus: true });
      }
      break;
    case "Tab":
      closeMenu({ notifyBlur: true });
      break;
    default:
      break;
  }
}

function handlePointerDown(event) {
  if (rootRef.value?.contains(event.target) || menuRef.value?.contains(event.target)) {
    return;
  }

  closeMenu({ notifyBlur: true });
}

function updatePosition() {
  if (!rootRef.value || !menuRef.value) {
    return;
  }

  computePosition(rootRef.value, menuRef.value, {
    placement: "bottom-start",
    middleware: [
      offset(6),
      flip({ padding: 12 }),
      shift({ padding: 12 }),
      size({
        apply({ rects, elements, availableHeight }) {
          Object.assign(elements.floating.style, {
            width: `${rects.reference.width}px`,
            maxHeight: `${Math.max(96, Math.min(availableHeight, 320))}px`,
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

watch(filteredOptions, (options) => {
  if (!isOpen.value) {
    return;
  }

  activeIndex.value = options.length ? Math.min(Math.max(activeIndex.value, 0), options.length - 1) : -1;
  nextTick(updatePosition);
});

// 候选被清空（换了中转站、重新拉取失败）时收起浮层，
// 否则会留下一个只显示空态的菜单挂在页面上。
watch(normalizedOptions, (options) => {
  if (options.length === 0) {
    closeMenu();
  }
});

watchPostEffect((cleanup) => {
  if (!isOpen.value || !rootRef.value || !menuRef.value) {
    return;
  }

  const stopAutoUpdate = autoUpdate(rootRef.value, menuRef.value, updatePosition);

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

watch(() => props.disabled, (disabled) => {
  if (disabled) {
    closeMenu();
  }
});

onBeforeUnmount(() => {
  document.removeEventListener("pointerdown", handlePointerDown);
});
</script>

<template>
  <div class="flex w-full min-w-0 items-center gap-2">
    <div
      ref="rootRef"
      class="flex h-9 min-w-0 flex-1 items-center rounded-[6px] border border-[#3f3f3f] bg-[#232323] transition-colors focus-within:border-[#10AD5D]"
    >
      <input
        ref="inputRef"
        :value="modelValue"
        type="text"
        role="combobox"
        autocomplete="off"
        autocapitalize="none"
        autocorrect="off"
        spellcheck="false"
        class="min-w-0 flex-1 bg-transparent px-3 text-sm text-[#e5e5e5] outline-none placeholder:text-[#7b7b7b] disabled:cursor-not-allowed disabled:opacity-60"
        :placeholder="placeholder"
        :disabled="disabled"
        :aria-label="ariaLabel || undefined"
        :aria-expanded="isOpen"
        :aria-controls="listboxID"
        :aria-activedescendant="activeDescendant"
        aria-autocomplete="list"
        aria-haspopup="listbox"
        @input="handleInput"
        @keydown="handleInputKeydown"
      />
      <button
        type="button"
        class="center-row h-full w-9 shrink-0 justify-center text-[#8f8f8f] outline-none transition-colors hover:text-[#d4d4d4] disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="disabled || loading"
        :aria-label="ariaLabel || undefined"
        :aria-expanded="isOpen"
        aria-haspopup="listbox"
        tabindex="-1"
        @mousedown.prevent
        @click="toggleMenu"
      >
        <span v-if="loading" class="icon-[mdi--loading] animate-spin text-[17px]"></span>
        <span
          v-else
          class="icon-[mdi--chevron-down] text-[18px] transition-transform duration-200"
          :class="isOpen ? 'rotate-180' : ''"
        ></span>
      </button>
    </div>
    <div v-if="$slots.append" class="shrink-0">
      <slot name="append" />
    </div>
  </div>

  <Teleport to="body">
    <Transition name="mo-pop">
      <div
        v-if="isOpen && normalizedOptions.length"
        :id="listboxID"
        ref="menuRef"
        role="listbox"
        class="fixed z-[999] overflow-hidden rounded-[8px] border border-[#3f3f3f] bg-[#232323] p-1.5 shadow-[0_16px_30px_-12px_rgba(0,0,0,0.7)]"
        :style="menuStyle"
      >
        <ul v-if="filteredOptions.length" class="overflow-y-auto">
          <li v-for="(option, index) in filteredOptions" :key="option.value">
            <button
              :id="`${listboxID}-option-${option.optionIndex}`"
              :ref="(el) => setOptionRef(el, option.optionIndex)"
              type="button"
              role="option"
              class="flex w-full items-center rounded-[6px] px-3 py-2 text-left text-sm outline-none transition-colors"
              :class="[
                option.value === modelValue
                  ? 'bg-[#10AD5D]/15 text-[#10d06f]'
                  : 'text-[#e5e5e5] hover:bg-[#303030]',
                activeIndex === index && option.value !== modelValue ? 'bg-[#303030]' : '',
              ]"
              :aria-selected="option.value === modelValue"
              tabindex="-1"
              @mousedown.prevent
              @click="selectOption(option)"
              @mouseenter="activeIndex = index"
            >
              <span class="flex min-w-0 items-center gap-2">
                <span v-if="option.icon" :class="[option.icon, 'shrink-0 text-[16px]']" aria-hidden="true"></span>
                <span class="truncate">{{ option.label }}</span>
              </span>
            </button>
          </li>
        </ul>
        <div v-else class="px-3 py-2 text-sm text-[#8f8f8f]">{{ emptyText }}</div>
      </div>
    </Transition>
  </Teleport>
</template>