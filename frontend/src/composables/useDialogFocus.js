import { nextTick, onScopeDispose, watch } from "vue";

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

// App.vue 里同时挂了普通弹窗、输入弹窗和更新提示弹窗，理论上可能叠加。
// 用一个栈保证 Escape 与焦点陷阱只作用于最上层的那个。
const dialogStack = [];

function topDialog() {
  return dialogStack.length > 0 ? dialogStack[dialogStack.length - 1] : null;
}

function focusableElements(container) {
  if (!container) {
    return [];
  }
  return Array.from(container.querySelectorAll(FOCUSABLE_SELECTOR)).filter(
    (element) => element.offsetParent !== null || element === document.activeElement,
  );
}

function handleKeydown(event) {
  const entry = topDialog();
  if (!entry) {
    return;
  }
  if (event.key === "Escape") {
    event.stopPropagation();
    event.preventDefault();
    entry.onEscape?.();
    return;
  }
  if (event.key !== "Tab") {
    return;
  }
  const items = focusableElements(entry.container());
  if (items.length === 0) {
    // 弹窗里没有可聚焦元素时，把焦点钉在容器上，别让 Tab 跑回背景内容。
    event.preventDefault();
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement;
  if (event.shiftKey && (active === first || !entry.container()?.contains(active))) {
    event.preventDefault();
    last.focus();
    return;
  }
  if (!event.shiftKey && (active === last || !entry.container()?.contains(active))) {
    event.preventDefault();
    first.focus();
  }
}

let keydownBound = false;

function ensureKeydownBound() {
  if (keydownBound || typeof document === "undefined") {
    return;
  }
  document.addEventListener("keydown", handleKeydown, true);
  keydownBound = true;
}

/**
 * useDialogFocus 给弹窗补上打开即聚焦、Escape 关闭、Tab 陷阱与关闭后焦点回归。
 *
 * @param {() => HTMLElement | null | undefined} container 返回弹窗根元素
 * @param {import("vue").Ref<boolean>} visible
 * @param {{ onEscape?: () => void, initialFocus?: () => HTMLElement | null | undefined }} [options]
 */
export function useDialogFocus(container, visible, options = {}) {
  const entry = {
    container,
    onEscape: options.onEscape,
  };
  let restoreTarget = null;

  function activate() {
    ensureKeydownBound();
    restoreTarget = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (!dialogStack.includes(entry)) {
      dialogStack.push(entry);
    }
    void nextTick(() => {
      const preferred = options.initialFocus?.();
      if (preferred) {
        preferred.focus();
        return;
      }
      focusableElements(container())[0]?.focus();
    });
  }

  function deactivate() {
    const index = dialogStack.indexOf(entry);
    if (index >= 0) {
      dialogStack.splice(index, 1);
    }
    // 只有当焦点还在（即将消失的）弹窗内部时才归还，避免抢走用户已经移开的焦点。
    const active = document.activeElement;
    if (restoreTarget && (!active || active === document.body || container()?.contains(active))) {
      restoreTarget.focus();
    }
    restoreTarget = null;
  }

  watch(
    visible,
    (next, previous) => {
      if (next === previous) {
        return;
      }
      if (next) {
        activate();
      } else {
        deactivate();
      }
    },
    { immediate: true },
  );

  onScopeDispose(() => {
    const index = dialogStack.indexOf(entry);
    if (index >= 0) {
      dialogStack.splice(index, 1);
    }
  });
}
