import { autoUpdate, computePosition, flip, offset as offsetMiddleware, shift } from "@floating-ui/dom";
import { nextTick, ref, watchPostEffect } from "vue";

/**
 * useFloating 封装 floating-ui 的定位与 autoUpdate 生命周期。
 *
 * 相对手写实现的关键差别是首帧：computePosition 是异步的，如果初始样式为空，
 * 浮层的第一帧会以 fixed + 无 left/top 的状态画出来（通常落在左上角），
 * 造成一次可见跳闪。这里初始就给 opacity:0 + 挪出视口，等第一次定位完成才显形。
 *
 * @param {() => HTMLElement | null | undefined} reference
 * @param {() => HTMLElement | null | undefined} floating
 * @param {import("vue").Ref<boolean>} isOpen
 * @param {{ placement?: string, offset?: number, padding?: number, extraMiddleware?: any[] }} [options]
 */
export function useFloating(reference, floating, isOpen, options = {}) {
  const { placement = "top", offset = 10, padding = 12, extraMiddleware = [] } = options;

  // 用 visibility 而不是 opacity 来藏首帧：浮层外面通常还套着一个做 opacity 过渡的
  // <Transition>，内联的 opacity 会盖掉过渡类，导致淡入失效变成硬闪。
  // visibility 与 opacity 互不干扰，且切换是瞬时的。
  const hiddenStyle = { position: "fixed", left: "0px", top: "0px", visibility: "hidden" };
  const floatingStyle = ref({ ...hiddenStyle });
  const currentPlacement = ref(placement);

  function reset() {
    floatingStyle.value = { ...hiddenStyle };
  }

  function update() {
    const referenceEl = reference();
    const floatingEl = floating();
    if (!referenceEl || !floatingEl) {
      return;
    }
    computePosition(referenceEl, floatingEl, {
      placement,
      middleware: [offsetMiddleware(offset), flip({ padding }), shift({ padding }), ...extraMiddleware],
    }).then(({ x, y, placement: resolvedPlacement }) => {
      currentPlacement.value = resolvedPlacement;
      floatingStyle.value = {
        position: "fixed",
        left: `${x}px`,
        top: `${y}px`,
        visibility: "visible",
        transformOrigin: resolvedPlacement.startsWith("top") ? "bottom" : "top",
      };
    });
  }

  watchPostEffect((cleanup) => {
    if (!isOpen.value) {
      reset();
      return;
    }
    void nextTick(update);
    const referenceEl = reference();
    const floatingEl = floating();
    if (!referenceEl || !floatingEl) {
      return;
    }
    const stop = autoUpdate(referenceEl, floatingEl, update);
    cleanup(() => {
      stop();
    });
  });

  return { floatingStyle, currentPlacement, update };
}
