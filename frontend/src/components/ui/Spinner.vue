<script setup>
// 复用 MessageProvider 已在用的图标，该图标已在 tailwind.config.cjs 的 iconSafelist 里，
// 所以这个组件不需要动 tailwind 配置。
defineProps({
  // 留空则跟随当前 font-size，按钮里内联时正好与文字等高。
  size: {
    type: String,
    default: "",
  },
  // 传了才暴露给读屏软件；纯装饰场景（旁边已有「测试中...」这类文案）保持 aria-hidden。
  label: {
    type: String,
    default: "",
  },
});
</script>

<template>
  <span
    class="icon-[mingcute--loading-fill] shrink-0 animate-spin"
    :class="size ? '' : 'text-[1em]'"
    :style="size ? { fontSize: size } : undefined"
    :role="label ? 'status' : undefined"
    :aria-label="label || undefined"
    :aria-hidden="label ? undefined : 'true'"
  ></span>
</template>
