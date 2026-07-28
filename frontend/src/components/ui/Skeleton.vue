<script setup>
// 骨架屏刻意不含任何文案：它替掉的「加载中...」是需要三语补译的字面量，
// 换成纯视觉占位反而净减 i18n key。
defineProps({
  variant: {
    type: String,
    default: "text",
    validator: (v) => ["text", "card", "chart", "row"].includes(v),
  },
  // text / row 的行数
  lines: {
    type: Number,
    default: 3,
  },
  // chart / card 的高度；留空则由外层容器决定
  height: {
    type: String,
    default: "",
  },
});
</script>

<template>
  <!-- aria-hidden：读屏软件不需要念占位块，加载语义由外层的 aria-busy 表达 -->
  <div class="animate-mo-skeleton" aria-hidden="true">
    <!-- 文本行：宽度递减，最后一行短一截，读起来像段落 -->
    <div v-if="variant === 'text'" class="flex flex-col gap-2">
      <div
        v-for="index in lines"
        :key="index"
        class="h-[12px] rounded-[4px] bg-[#2c2c2c]"
        :style="{ width: index === lines ? '55%' : `${100 - index * 6}%` }"
      ></div>
    </div>

    <!-- 卡片：撑住与真实卡片一致的最小高度，避免 grid 列数与行高跳变 -->
    <div
      v-else-if="variant === 'card'"
      class="flex flex-col gap-3 rounded-[8px] border border-[#343434] bg-[#252525] p-3"
      :style="height ? { minHeight: height } : undefined"
    >
      <div class="h-[14px] w-[45%] rounded-[4px] bg-[#2c2c2c]"></div>
      <div class="grid grid-cols-2 gap-2">
        <div class="h-[38px] rounded-[8px] bg-[#232323]"></div>
        <div class="h-[38px] rounded-[8px] bg-[#232323]"></div>
      </div>
      <div class="h-[30px] rounded-[8px] bg-[#232323]"></div>
    </div>

    <!-- 图表：Chart.js 的 canvas 没有内在尺寸，这一格才是真正防布局跳动的那个 -->
    <div
      v-else-if="variant === 'chart'"
      class="flex items-end gap-2 rounded-[8px] bg-[#232323] p-3"
      :style="{ height: height || '100%' }"
    >
      <div
        v-for="index in 12"
        :key="index"
        class="flex-1 rounded-t-[3px] bg-[#2f2f2f]"
        :style="{ height: `${25 + ((index * 37) % 60)}%` }"
      ></div>
    </div>

    <!-- 行：标签 + 数值 + 占比条，对应排行榜的一行 -->
    <div v-else class="flex flex-col gap-2">
      <div v-for="index in lines" :key="index" class="flex flex-col gap-1.5 rounded-[6px] px-2 py-1.5">
        <div class="center-row justify-between gap-2">
          <div class="h-[11px] rounded-[4px] bg-[#2c2c2c]" :style="{ width: `${45 - index * 4}%` }"></div>
          <div class="h-[11px] w-[52px] rounded-[4px] bg-[#2c2c2c]"></div>
        </div>
        <div class="h-[6px] rounded-full bg-[#232323]">
          <div class="h-full rounded-full bg-[#2f2f2f]" :style="{ width: `${80 - index * 12}%` }"></div>
        </div>
      </div>
    </div>
  </div>
</template>
