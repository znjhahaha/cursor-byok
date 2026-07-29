const { addDynamicIconSelectors } = require("@iconify/tailwind");

const iconSafelist = [
  "icon-[ant-design--bilibili-outlined]",
  "icon-[bxl--openai]",
  "icon-[cil--badge]",
  "icon-[dashicons--yes]",
  "icon-[ic--round-close]",
  "icon-[ic--round-minus]",
  "icon-[logos--claude-icon]",
  "icon-[mdi--api]",
  "icon-[mdi--alert-circle-outline]",
  "icon-[mdi--brain]",
  "icon-[mdi--chart-bar]",
  "icon-[mdi--chart-line]",
  "icon-[mdi--chart-pie]",
  "icon-[mdi--check]",
  "icon-[mdi--check-circle-outline]",
  "icon-[mdi--chevron-down]",
  "icon-[mdi--chevron-up]",
  "icon-[mdi--content-copy]",
  "icon-[mdi--counter]",
  "icon-[mdi--eye-off-outline]",
  "icon-[mdi--eye-outline]",
  "icon-[mdi--file-document-outline]",
  "icon-[mdi--head-cog-outline]",
  "icon-[mdi--head-lightbulb-outline]",
  "icon-[mdi--head-outline]",
  "icon-[mdi--information-outline]",
  "icon-[mdi--layers-outline]",
  "icon-[mdi--magnify]",
  "icon-[mdi--message-text-outline]",
  "icon-[mdi--pause]",
  "icon-[mdi--play]",
  "icon-[mdi--plus]",
  "icon-[mdi--refresh]",
  "icon-[mdi--robot-outline]",
  "icon-[mdi--server-network]",
  "icon-[mdi--trending-down]",
  "icon-[mdi--trending-neutral]",
  "icon-[mdi--trending-up]",
  "icon-[mdi--web]",
  "icon-[mdi--wifi]",
  "icon-[mingcute--loading-fill]",
];

module.exports = {
  content: ["./index.html", "./src/**/*.{vue,js,jsx,ts,tsx}"],
  safelist: [...iconSafelist, "z-999", "z-9999", "z-99999"],
  theme: {
    extend: {
      colors: {
        primary: {
          50: "#f0f7ff",
          100: "#e6f4ff",
          200: "#bae0ff",
          300: "#91caff",
          400: "#69b1ff",
          500: "#4096ff",
          600: "#1677ff",
          700: "#0958d9",
          800: "#003eb3",
          900: "#002c8c",
          950: "#001d66",
          DEFAULT: "#1677ff",
        },
      },
      fontFamily: {
        num: [
          "HFKos",
          "PingFang-Medium",
          "system-ui",
          "-apple-system",
          "BlinkMacSystemFont",
          "\"Segoe UI\"",
          "Roboto",
          "sans-serif",
        ],
      },
      fontSize: {
        xs: ["12px", { lineHeight: "16px" }],
        sm: ["13px", { lineHeight: "18px" }],
        lg: ["20px", { lineHeight: "28px" }],
      },
      zIndex: {
        999: "999",
        9999: "9999",
        99999: "99999",
      },
      // 动效 token 全部指向 src/style/motion.css 里的 CSS 变量。
      // 覆盖 DEFAULT 是关键：Tailwind 给每个 transition-* utility 都内联了
      // transitionDuration.DEFAULT 与 transitionTimingFunction.DEFAULT，
      // 所以这两行等于一次性接管了代码里全部裸 `transition-*` 的时长与曲线，
      // prefers-reduced-motion 因此只要改变量、不用改模板。
      //
      // 每个值都带 fallback：万一 motion.css 没被引入，var() 解析失败会让
      // transition-duration 退化成初始值 0s，所有 hover 过渡会静默消失。
      transitionDuration: {
        DEFAULT: "var(--mo-hover, 150ms)",
        100: "var(--mo-quick, 100ms)",
        150: "var(--mo-hover, 150ms)",
        200: "var(--mo-enter, 200ms)",
        fast: "var(--mo-fast, 140ms)",
        leave: "var(--mo-leave, 160ms)",
        mask: "var(--mo-mask, 180ms)",
        enter: "var(--mo-enter, 200ms)",
        panel: "var(--mo-panel, 220ms)",
      },
      transitionTimingFunction: {
        DEFAULT: "var(--mo-ease, cubic-bezier(0, 0, 0.2, 1))",
        out: "var(--mo-ease, cubic-bezier(0, 0, 0.2, 1))",
        in: "var(--mo-ease-in, cubic-bezier(0.4, 0, 1, 1))",
      },
      transitionDelay: {
        stagger: "var(--mo-stagger, 60ms)",
      },
      transitionProperty: {
        // transition-colors 不含 opacity —— 所以凡是配了 disabled:opacity-* 的地方
        // 都必须用这个，否则禁用态是瞬间变灰。
        interactive: "color, background-color, border-color, opacity, transform, box-shadow",
        size: "grid-template-rows, height, width, margin, opacity",
      },
      keyframes: {
        "mo-skeleton": {
          "0%, 100%": { opacity: "0.45" },
          "50%": { opacity: "0.85" },
        },
      },
      animation: {
        "mo-skeleton": "mo-skeleton 1.4s linear infinite",
      },
    },
  },
  plugins: [addDynamicIconSelectors()],
};
