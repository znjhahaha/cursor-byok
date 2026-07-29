import { Window } from "@wailsio/runtime";
import { onBeforeUnmount } from "vue";

// 主窗口临时加高的状态机：记住加高前的高度，只在「确实是我们加高的」前提下恢复，
// 避免覆盖用户手动拖出来的窗口尺寸。
// 调用方负责给出固定增量（如趋势区比总览区高出的 66px），本模块只保证成对执行。
export function useWindowGrow() {
  let grown = false;
  let savedHeight = 0;
  // 快速来回切换时 Size/SetSize 是异步 IPC，串行化防止乱序把高度写错。
  let pending = Promise.resolve();

  function enqueue(task) {
    pending = pending.then(task, task);
    return pending;
  }

  function grow(delta) {
    if (!Number.isFinite(delta) || delta <= 0) {
      return Promise.resolve();
    }
    return enqueue(async () => {
      if (grown) {
        return;
      }
      try {
        const size = await Window.Size();
        if (!size || size.height <= 0) {
          return;
        }
        savedHeight = size.height;
        await Window.SetSize(size.width, size.height + delta);
        grown = true;
      } catch (_error) {
        // 窗口 API 不可用时保持现状，不影响界面内容。
      }
    });
  }

  function restore() {
    return enqueue(async () => {
      if (!grown) {
        return;
      }
      grown = false;
      try {
        const size = await Window.Size();
        await Window.SetSize(size.width, savedHeight);
      } catch (_error) {
        // 同上：恢复失败只是窗口多留一段高度，用户可手动调整。
      }
    });
  }

  onBeforeUnmount(() => {
    void restore();
  });

  return { grow, restore };
}