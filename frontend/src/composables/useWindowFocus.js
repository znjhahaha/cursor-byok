import { Events } from "@wailsio/runtime";
import { onScopeDispose, readonly, ref } from "vue";

// 模块级单例：每个窗口只订阅一次，多个组件共享同一份状态。
const isFocused = ref(true);
const regainListeners = new Set();
let started = false;
let settleTimer = 0;

// 为什么必须以「状态真的翻转了」为闸门，而不是裸监听事件：
// Wails 在 Windows 上处理 WM_ENTERSIZEMOVE 时会显式 SetFocus，于是**拖动窗口**
// 也会发出 WindowFocus。而这个应用的标题栏满是 --wails-draggable，
// 裸事件驱动的「回焦刷新」会变成「拖一下就刷一次」。
function setFocused(next) {
  window.clearTimeout(settleTimer);
  // 60ms 去抖：chromium 子窗口交还焦点时会出现成对的 blur/focus。
  settleTimer = window.setTimeout(() => {
    if (isFocused.value === next) {
      return;
    }
    isFocused.value = next;
    if (!next) {
      return;
    }
    for (const listener of regainListeners) {
      try {
        listener();
      } catch (_error) {
        // 单个订阅者失败不应影响其它订阅者。
      }
    }
  }, 60);
}

function start() {
  if (started || typeof window === "undefined") {
    return;
  }
  started = true;

  // 主路径：Wails 窗口事件。三个子窗口共用 buildChildWindowOptions，
  // 没有覆盖 EventMapping，所以同样能收到。
  try {
    Events.On("common:WindowFocus", () => setFocused(true));
    Events.On("common:WindowLostFocus", () => setFocused(false));
  } catch (_error) {
    // 非 Wails 环境（例如直接用浏览器开 vite dev）下走下面的 DOM 兜底。
  }

  // 兜底路径：DOM 事件。构造上 fail-open —— 两条路径都收不到时
  // isFocused 恒为 true，行为等同于改动之前。
  window.addEventListener("focus", () => setFocused(true));
  window.addEventListener("blur", () => {
    // 广告 iframe 拿到焦点时 window 也会 blur，用 hasFocus 过滤掉。
    setFocused(document.hasFocus());
  });
  document.addEventListener("visibilitychange", () => setFocused(!document.hidden));
}

/**
 * useWindowFocus 返回当前窗口是否聚焦。
 *
 * @param {() => void} [onRegainFocus] 仅在「失焦 → 回焦」真实跃变时调用
 */
export function useWindowFocus(onRegainFocus) {
  start();
  if (onRegainFocus) {
    regainListeners.add(onRegainFocus);
    onScopeDispose(() => {
      regainListeners.delete(onRegainFocus);
    });
  }
  return readonly(isFocused);
}
