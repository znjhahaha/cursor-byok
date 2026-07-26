const TOOLTIP_CLASS = "cursor-usage-external-tooltip";

function createTooltipElement() {
  const element = document.createElement("div");
  element.className = TOOLTIP_CLASS;
  Object.assign(element.style, {
    position: "fixed",
    zIndex: "99999",
    display: "none",
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
    pointerEvents: "none",
    whiteSpace: "normal",
    overflowWrap: "anywhere",
  });
  document.body.appendChild(element);
  return element;
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

// Chart.js 的默认 tooltip 绘制在 canvas 内，高度受限时会被裁掉。
// 这里把展示层搬到 body，Chart.js 仍负责命中、内容回调和 caret 坐标。
export function createExternalTooltip() {
  let element = null;

  function handler({ chart, tooltip }) {
    if (typeof document === "undefined") {
      return;
    }
    if (!element) {
      element = createTooltipElement();
    }
    if (!tooltip || tooltip.opacity === 0) {
      element.style.display = "none";
      return;
    }

    element.replaceChildren();
    for (const line of tooltip.title ?? []) {
      appendLine(element, line, { title: true });
    }
    for (const line of tooltip.beforeBody ?? []) {
      appendLine(element, line, { strong: true });
    }
    for (const line of bodyLines(tooltip)) {
      appendLine(element, line);
    }
    for (const line of tooltip.afterBody ?? []) {
      appendLine(element, line);
    }

    element.style.display = "block";
    element.style.left = "0px";
    element.style.top = "0px";

    const canvasRect = chart.canvas.getBoundingClientRect();
    const tooltipRect = element.getBoundingClientRect();
    const gap = 10;
    let left = canvasRect.left + tooltip.caretX + gap;
    let top = canvasRect.top + tooltip.caretY - tooltipRect.height / 2;

    if (left + tooltipRect.width > window.innerWidth - 8) {
      left = canvasRect.left + tooltip.caretX - tooltipRect.width - gap;
    }
    if (top + tooltipRect.height > window.innerHeight - 8) {
      top = window.innerHeight - tooltipRect.height - 8;
    }
    top = Math.max(8, top);
    left = Math.max(8, Math.min(left, window.innerWidth - tooltipRect.width - 8));

    element.style.left = `${left}px`;
    element.style.top = `${top}px`;
  }

  function destroy() {
    element?.remove();
    element = null;
  }

  return { handler, destroy };
}