import { reactive } from "vue";

export const modalState = reactive({
  visible: false,
  title: "提示",
  content: "",
  confirmText: "确定",
  cancelText: "取消",
  showCancel: true,
  confirmDisabled: false,
  _resolve: null,
});

/**
 * 显示确认弹窗，返回 Promise<boolean>
 * @param {Object} options - { title, content }
 * @returns {Promise<boolean>} - true=确定, false=取消
 */
export function showModal(options = {}) {
  return new Promise((resolve) => {
    // 上一个弹窗还没结算就被覆盖时，先按「取消」把它结算掉。
    // 否则它的 Promise 永远不 settle，await 它的调用方会永久挂住 ——
    // 加了 Escape 关闭之后用户更容易快速连开弹窗，这条会更容易被触发。
    const pending = modalState._resolve;
    modalState._resolve = null;
    pending?.(false);

    modalState.visible = true;
    modalState.title = options.title ?? "提示";
    modalState.content = options.content ?? "";
    modalState.confirmText = options.confirmText ?? "确定";
    modalState.cancelText = options.cancelText ?? "取消";
    modalState.showCancel = options.showCancel ?? true;
    modalState.confirmDisabled = options.confirmDisabled ?? false;
    modalState._resolve = resolve;
  });
}

export function resolveModal(ok) {
  modalState.visible = false;
  const resolve = modalState._resolve;
  modalState._resolve = null;
  resolve?.(ok);
}
