<script setup>
import Spinner from "@/components/ui/Spinner.vue";
import { computed } from "vue";

const props = defineProps({
  variant: {
    type: String,
    default: "default",
    validator: (v) => ["default", "primary", "text"].includes(v),
  },
  // disabled 之前是靠 attribute fallthrough 落到根 button 上的，功能正常但没有任何视觉。
  // 一旦声明成 prop，它就从 $attrs 里被摘走，所以下面两个 button 都必须显式绑定 ——
  // 漏掉任何一个，全部调用点的禁用态都会静默失效（保存过程中按钮会变得可点）。
  disabled: {
    type: Boolean,
    default: false,
  },
  loading: {
    type: Boolean,
    default: false,
  },
});

const isBlocked = computed(() => props.disabled || props.loading);
</script>

<template>
  <button
    v-if="variant === 'text'"
    type="button"
    :disabled="isBlocked"
    :aria-busy="loading || undefined"
    class="!whitespace-nowrap center-row shrink-0 cursor-pointer gap-[4px] text-sm text-[#a3a3a3] transition-interactive duration-150 active:text-[#10AD5D] hover:text-[#10ad5cd9] focus-visible:rounded-[4px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35 disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-60"
  >
    <Spinner v-if="loading" />
    <slot />
  </button>
  <button
    v-else
    type="button"
    :disabled="isBlocked"
    :aria-busy="loading || undefined"
    class="!whitespace-nowrap relative cursor-pointer overflow-hidden center-row min-h-[24px] gap-[2px] rounded-[6px] text-sm transition-interactive duration-150 active:scale-[0.97] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35 disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-60 disabled:active:scale-100"
    :class="{
      'bg-[linear-gradient(to_bottom,#656565_0%,#3A3A3A_10px,#3A3A3A_100%)]': variant === 'default',
      'bg-gradient-to-b from-[#1D8010] to-[#25B433]': variant === 'primary',
    }"
  >
    <!-- 外层亮渐变 + 内层暗渐变覆盖，是用两层背景模拟 1px 描边。
         隐含前提是内层必须在垂直方向盖满外层：文字按钮行高 18px 加 py-[3px] 恰好等于
         min-h-[24px]，所以一直没暴露问题；而纯图标插槽只有 14px，内层 20px，
         居中后上下各露 2px 外层渐变，顶部那条 #656565 就是可见的高光。
         self-stretch 让内层在交叉轴拉满，图标态与文字态表现一致。 -->
    <span
      class="relative center-row z-10 min-h-[24px] w-full self-stretch justify-center gap-[4px] rounded-[5px] !px-[7px] py-[3px] text-white transition-colors"
      :class="{
        'bg-gradient-to-b from-[#2a2a2a] to-[#1f1f1f] ': variant === 'default',
        'font-medium bg-gradient-to-b from-[#10AD5D] to-[#0F8A4C] ': variant === 'primary',
      }"
    >
      <Spinner v-if="loading" />
      <slot />
    </span>
  </button>
</template>
