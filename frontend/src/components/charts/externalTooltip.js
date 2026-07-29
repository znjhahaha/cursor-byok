const TOOLTIP_CLASS = "cursor-usage-external-tooltip";
const TOOLTIP_CONTENT_CLASS = `${TOOLTIP_CLASS}__content`;

// 定位与视觉必须拆层：进场/离场只动 opacity，进场瞬间 transform 直写（snap），
// 否则 Chart.js 每帧回调都会把坐标更新读成「从旧点飞到新点」。
// 已显示期间的 caret 移动则给 transform 一段短过渡，形成平滑跟随——
// 这正是「snap 进场 + 跟随移动」两条路径分开管 transition 的原因。
const ENTER_TRANSITION = "opacity var(--mo-fast) var(--mo-ease)";
const LEAVE_TRANSITION = "opacity var(--mo-quick) var(--mo-ease-in)";
const MOVE_TRANSITION =
  "transform 160ms var(--mo-ease), opacity var(--mo-fast) var(--mo-ease)";

function createTooltipElement() {
  const element = document.createElement("div");
  const content = document.createElement("div");
  element.className = TOOLTIP_CLASS;
  content.className = TOOLTIP_CONTENT_CLASS;
  Object.assign(element.style, {
    position: "fixed",
    left: "0",
    top: "0",
    zIndex: "99999",
    opacity: "0",
    pointerEvents: "none",
    willChange: "opacity, transform",
  });
  // 单层内容：换数据点时瞬时替换文字与尺寸，不做交叉/位移/宽高动画。
  // 双缓冲 + size morph + translateY 叠在一起就是上一版「一抖一抖」的来源。
  Object.assign(content.style, {
    maxWidth: "280px",
    padding: "10px 12px",
    border: "1px solid #3d3d3d",
    borderRadius: "8px",
    background: "#2b2b2b",
    boxShadow: "0 12px 30px rgba(0, 0, 0, 0.45)",
    color: "#d4d4d4",
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif",
    fontSize: "12px",
    lineHeight: "1.55",
    whiteSpace: "normal",
    overflowWrap: "anywhere",
  });
  element.appendChild(content);
  document.body.appendChild(element);
  return { element, content };
}

function appendLine(root, text, { title = false, strong = false } = {}) {
  if (!text) {
    return;
  }
  const line = document.createElement("div");
  line.textContent = text;
  if (title) {
    line.style.color = "#f5f5f5";
    line.style.fontWeight = "600";
    line.style.marginBottom = "4px";
  } else if (strong) {
    line.style.color = "#e5e5e5";
    line.style.fontWeight = "500";
    line.style.marginBottom = "2px";
  }
  root.appendChild(line);
}

function bodyLines(tooltip) {
  return (tooltip.body ?? []).flatMap((item) => item.lines ?? []);
}

function tooltipContentKey(tooltip) {
  return JSON.stringify([
    tooltip.title ?? [],
    tooltip.beforeBody ?? [],
    bodyLines(tooltip),
    tooltip.afterBody ?? [],
  ]);
}

function renderTooltipContent(content, tooltip) {
  content.replaceChildren();
  for (const line of tooltip.title ?? []) {
    appendLine(content, line, { title: true });
  }
  for (const line of tooltip.beforeBody ?? []) {
    appendLine(content, line, { strong: true });
  }
  for (const line of bodyLines(tooltip)) {
    appendLine(content, line);
  }
  for (const line of tooltip.afterBody ?? []) {
    appendLine(content, line);
  }
}

// Chart.js 的默认 tooltip 绘制在 canvas 内，高度受限时会被裁掉。
// 这里把展示层搬到 body，Chart.js 仍负责命中、内容回调和 caret 坐标。
export function createExternalTooltip() {
  let element = null;
  let content = null;
  let visible = false;
  let contentKey = "";

  function handler({ chart, tooltip }) {
    if (typeof document === "undefined") {
      return;
    }
    if (!element) {
      ({ element, content } = createTooltipElement());
    }
    if (!tooltip || tooltip.opacity === 0) {
      element.style.transition = LEAVE_TRANSITION;
      element.style.opacity = "0";
      visible = false;
      return;
    }

    const nextContentKey = tooltipContentKey(tooltip);
    if (nextContentKey !== contentKey) {
      renderTooltipContent(content, tooltip);
      contentKey = nextContentKey;
    }

    // 尺寸从内容盒读取；offsetWidth/Height 不受外层 translate 影响。
    const canvasRect = chart.canvas.getBoundingClientRect();
    const tooltipWidth = content.offsetWidth;
    const tooltipHeight = content.offsetHeight;

    const gap = 10;
    let left = canvasRect.left + tooltip.caretX + gap;
    let top = canvasRect.top + tooltip.caretY - tooltipHeight / 2;

    if (left + tooltipWidth > window.innerWidth - 8) {
      left = canvasRect.left + tooltip.caretX - tooltipWidth - gap;
    }
    if (top + tooltipHeight > window.innerHeight - 8) {
      top = window.innerHeight - tooltipHeight - 8;
    }
    top = Math.max(8, top);
    left = Math.max(8, Math.min(left, window.innerWidth - tooltipWidth - 8));

    // 位置更新先直写：进场路径靠 transition:none 保持 snap；
    // 已显示路径则由下面的 MOVE_TRANSITION 把这次更新插值成跟随动画。
    element.style.transform = `translate3d(${Math.round(left)}px, ${Math.round(top)}px, 0)`;

    if (!visible) {
      element.style.transition = "none";
      element.style.opacity = "0";
      void element.offsetWidth;
      element.style.transition = ENTER_TRANSITION;
      element.style.opacity = "1";
      visible = true;
    } else {
      element.style.transition = MOVE_TRANSITION;
    }
  }

  function destroy() {
    element?.remove();
    element = null;
    content = null;
    visible = false;
    contentKey = "";
  }

  return { handler, destroy };
}