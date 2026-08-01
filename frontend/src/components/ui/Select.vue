<script setup>
import { autoUpdate, computePosition, flip, offset, shift, size } from "@floating-ui/dom";
import { computed, nextTick, onBeforeUnmount, ref, watch, watchPostEffect } from "vue";

const props = defineProps({
  modelValue: { type: String, default: "" },
  options: {
    type: Array,
    default: () => [],
  },
  placeholder: { type: String, default: "请选择" },
  disabled: { type: Boolean, default: false },
  border: { type: Boolean, default: true },
  // 只显示一个图标的触发器：用于卡片操作行这类横向空间紧张的场景。
  // 设了它就不再渲染文案与 chevron，宽度由图标本身决定，
  // 调用方必须同时给 ariaLabel，否则触发器对读屏软件是空的。
  triggerIcon: { type: String, default: "" },
  ariaLabel: { type: String, default: "" },
  buttonClass: { type: String, default: "" },
  menuClass: { type: String, default: "" },
});

const emit = defineEmits(["update:modelValue", "change", "blur"]);

const rootRef = ref(null);
const buttonRef = ref(null);
const menuRef = ref(null);
const optionRefs = ref([]);
const isOpen = ref(false);
const activeIndex = ref(-1);
const menuStyle = ref({});

const normalizedOptions = computed(() => props.options.map((option) => {
  if (typeof option === "string") {
    return { label: option, value: option };
  }

  return {
    label: option?.label ?? option?.value ?? "",
    value: option?.value ?? "",
    icon: option?.icon ?? option?.iconClass ?? "",
    danger: Boolean(option?.danger),
  };
}));

const selectedOption = computed(() => normalizedOptions.value.find((option) => option.value === props.modelValue) ?? null);
const selectedLabel = computed(() => selectedOption.value?.label || props.placeholder);

function setOptionRef(el, index) {
  if (el) {
    optionRefs.value[index] = el;
    return;
  }

  delete optionRefs.value[index];
}

function focusActiveOption() {
  nextTick(() => {
    const option = optionRefs.value[activeIndex.value];
    option?.focus();
  });
}

function openMenu() {
  if (props.disabled || isOpen.value) {
    return;
  }

  isOpen.value = true;
  const selectedIndex = normalizedOptions.value.findIndex((option) => option.value === props.modelValue);
  activeIndex.value = selectedIndex >= 0 ? selectedIndex : 0;
  nextTick(() => {
    updatePosition();
    focusActiveOption();
  });
}

function closeMenu({ restoreFocus = false } = {}) {
  if (!isOpen.value) {
    return;
  }

  isOpen.value = false;
  activeIndex.value = -1;
  optionRefs.value = [];
  menuStyle.value = {};

  if (restoreFocus) {
    nextTick(() => buttonRef.value?.focus());
  }

  emit("blur");
}

function toggleMenu() {
  if (isOpen.value) {
    closeMenu();
    return;
  }

  openMenu();
}

function selectOption(option) {
  if (!option) {
    closeMenu({ restoreFocus: true });
    return;
  }

  // 同值再点也发 change：自定义天数等场景需要「再选一次重新编辑」。
  if (option.value === props.modelValue) {
    emit("change", option.value);
    closeMenu({ restoreFocus: true });
    return;
  }

  emit("update:modelValue", option.value);
  emit("change", option.value);
  closeMenu({ restoreFocus: true });
}

function moveActiveIndex(step) {
  if (!normalizedOptions.value.length) {
    return;
  }

  if (!isOpen.value) {
    openMenu();
    return;
  }

  const total = normalizedOptions.value.length;
  const current = activeIndex.value >= 0 ? activeIndex.value : 0;
  activeIndex.value = (current + step + total) % total;
  focusActiveOption();
}

function handleButtonKeydown(event) {
  if (props.disabled) {
    return;
  }

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
    case " ":
      event.preventDefault();
      toggleMenu();
      break;
    case "Escape":
      if (isOpen.value) {
        event.preventDefault();
        closeMenu();
      }
      break;
    default:
      break;
  }
}

function handleOptionKeydown(event, option, index) {
  switch (event.key) {
    case "ArrowDown":
      event.preventDefault();
      activeIndex.value = index;
      moveActiveIndex(1);
      break;
    case "ArrowUp":
      event.preventDefault();
      activeIndex.value = index;
      moveActiveIndex(-1);
      break;
    case "Enter":
    case " ":
      event.preventDefault();
      selectOption(option);
      break;
    case "Escape":
      event.preventDefault();
      closeMenu({ restoreFocus: true });
      break;
    case "Tab":
      closeMenu();
      break;
    default:
      break;
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
    // 图标触发器通常贴在卡片右下角，从左沿展开会直接怼出容器；
    // 让菜单右对齐再由 shift 兜底。
    placement: props.triggerIcon ? "bottom-end" : "bottom-start",
    middleware: [
      offset(8),
      flip({ padding: 12 }),
      shift({ padding: 12 }),
      size({
        apply({ rects, elements, availableHeight }) {
          Object.assign(elements.floating.style, {
            minWidth: props.triggerIcon ? "168px" : `${rects.reference.width}px`,
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

watchPostEffect((cleanup) => {
  if (!isOpen.value || !buttonRef.value || !menuRef.value) {
    return;
  }

  const stopAutoUpdate = autoUpdate(buttonRef.value, menuRef.value, updatePosition);

  cleanup(() => {
    stopAutoUpdate();
  });
});

watch(() => props.modelValue, () => {
  if (!isOpen.value) {
    return;
  }

  const selectedIndex = normalizedOptions.value.findIndex((option) => option.value === props.modelValue);
  activeIndex.value = selectedIndex >= 0 ? selectedIndex : 0;
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
      class="flex items-center rounded-[6px] bg-[#232323] text-left text-sm text-[#e5e5e5] outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-60"
      :class="[
        triggerIcon
          ? 'h-6 w-6 shrink-0 justify-center border border-[#3f3f3f] text-[#a3a3a3] hover:text-[#e5e5e5] focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35'
          : 'h-9 px-3',
        !triggerIcon && border
          ? 'w-full justify-between gap-2 border border-[#3f3f3f] focus:border-[#10AD5D]'
          : '',
        !triggerIcon && !border
          ? 'w-auto justify-start gap-2 border border-transparent focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35'
          : '',
        buttonClass,
      ]"
      :aria-expanded="isOpen"
      :aria-label="ariaLabel || undefined"
      aria-haspopup="listbox"
      @click="toggleMenu"
      @keydown="handleButtonKeydown"
    >
      <span v-if="triggerIcon" :class="[triggerIcon, 'text-[16px]']" aria-hidden="true"></span>
      <template v-else>
        <span
          class="flex min-w-0 items-center gap-2"
          :class="[
            border ? 'flex-1' : 'shrink-0',
            selectedOption
              ? (border ? 'text-[#e5e5e5]' : 'text-current')
              : 'text-[#7b7b7b]',
          ]"
        >
          <span v-if="selectedOption?.icon" :class="[selectedOption.icon, 'text-[16px] shrink-0']" aria-hidden="true"></span>
          <span class="truncate">{{ selectedLabel }}</span>
        </span>
        <span
          class="pointer-events-none center-row transition-transform duration-200"
          :class="[border ? 'text-[#8f8f8f]' : 'text-current', isOpen ? 'rotate-180' : '']"
        >
          <span class="icon-[mdi--chevron-down] text-[18px]"></span>
        </span>
      </template>
    </button>
  </div>

  <Teleport to="body">
    <!-- 之前的 leave 类漏了 scale，导致菜单「缩着进、不缩着出」。
         共享的 mo-pop 两端对称。 -->
    <Transition name="mo-pop">
      <div
        v-if="isOpen"
        ref="menuRef"
        class="fixed z-[999] overflow-hidden rounded-[8px] border border-[#3f3f3f] bg-[#232323] p-1.5 shadow-[0_16px_30px_-12px_rgba(0,0,0,0.7)]"
        :class="menuClass"
        :style="menuStyle"
      >
        <!-- 内边距只由容器的 p-1.5 提供：ul 上再叠 py-1 会让选中项上下 10px、左右 6px。 -->
        <ul role="listbox" class="overflow-y-auto">
          <li v-for="(option, index) in normalizedOptions" :key="option.value">
            <button
              :ref="(el) => setOptionRef(el, index)"
              type="button"
              role="option"
              class="flex w-full items-center rounded-[6px] px-3 py-2 text-left text-sm outline-none transition-colors"
              :class="[
                option.value === modelValue
                  ? 'bg-[#10AD5D]/15 text-[#10d06f]'
                  : option.danger
                    ? 'text-[#f87171] hover:bg-[#3a2626]'
                    : 'text-[#e5e5e5] hover:bg-[#303030]',
                activeIndex === index ? (option.danger ? 'bg-[#3a2626]' : 'bg-[#303030]') : '',
                // 危险项与常规项之间留一条分隔线，避免「删除」紧贴普通操作被误点。
                option.danger ? 'mt-1 border-t border-[#3f3f3f] pt-2' : '',
              ]"
              :aria-selected="option.value === modelValue"
              tabindex="0"
              @click="selectOption(option)"
              @mouseenter="activeIndex = index"
              @keydown="handleOptionKeydown($event, option, index)"
            >
              <span class="flex min-w-0 items-center gap-2">
                <span v-if="option.icon" :class="[option.icon, 'text-[16px] shrink-0']" aria-hidden="true"></span>
                <span class="truncate">{{ option.label }}</span>
              </span>
            </button>
          </li>
        </ul>
      </div>
    </Transition>
  </Teleport>
</template>
