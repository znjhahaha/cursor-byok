<script setup>
import { messageState, provideMessage } from "@/composables/useMessage";

provideMessage();

const MESSAGE_THEME = {
  success: {
    containerClass: "bg-[#10AD5D] text-white",
    iconClass: "icon-[dashicons--yes]",
    iconExtraClass: "",
  },
  // 失败提示是最需要被一眼认出的一类，之前反而是唯一没有图标的。
  error: {
    containerClass: "bg-[#D84C4C] text-white",
    iconClass: "icon-[mdi--alert-circle-outline]",
    iconExtraClass: "",
  },
  info: {
    containerClass: "bg-[#F08A24] text-white",
    iconClass: "icon-[mdi--information-outline]",
    iconExtraClass: "",
  },
  loading: {
    containerClass: "bg-[#3a3a3a] text-white",
    iconClass: "icon-[mingcute--loading-fill]",
    iconExtraClass: "animate-spin",
  },
};

function resolveTheme(type) {
  return MESSAGE_THEME[type] || MESSAGE_THEME.info;
}
</script>

<template>
  <div
    class="pointer-events-none fixed inset-x-0 top-4 z-[1000] flex justify-center px-4"
    role="status"
    aria-live="polite"
    aria-atomic="true"
  >
    <!-- 不用 mode="out-in"：它会串行化离场与入场，叠上 useMessage 的 MIN_VISIBLE_MS=300
         之后连续两条提示会有明显停顿。改成绝对定位叠放，让入离场重叠。 -->
    <Transition name="mo-fade-down">
      <div
        v-if="messageState.current"
        :key="messageState.current.id"
        class="pointer-events-auto absolute inline-flex max-w-full items-center gap-2 rounded-full px-4 py-2 text-sm shadow-[0_8px_24px_rgba(0,0,0,0.28)]"
        :class="resolveTheme(messageState.current.type).containerClass"
      >
        <span
          v-if="resolveTheme(messageState.current.type).iconClass"
          class="text-[14px]"
          :class="[
            resolveTheme(messageState.current.type).iconClass,
            resolveTheme(messageState.current.type).iconExtraClass,
          ]"
        />
        <span class="leading-none whitespace-nowrap">{{ messageState.current.content }}</span>
      </div>
    </Transition>
  </div>
</template>





