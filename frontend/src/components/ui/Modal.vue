<script setup>
import Button from "@/components/ui/Button.vue";
import { useDialogFocus } from "@/composables/useDialogFocus";
import { computed, ref, toRef } from "vue";

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: "提示" },
  content: { type: String, default: "" },
  confirmText: { type: String, default: "确定" },
  cancelText: { type: String, default: "取消" },
  showCancel: { type: Boolean, default: true },
  confirmDisabled: { type: Boolean, default: false },
  confirmLoading: { type: Boolean, default: false },
});

const emit = defineEmits(["update:visible", "confirm", "cancel"]);

const dialogRef = ref(null);
const titleID = `mo-dialog-title-${Math.random().toString(36).slice(2, 9)}`;

function handleConfirm() {
  emit("confirm");
  emit("update:visible", false);
}

function handleCancel() {
  emit("cancel");
  emit("update:visible", false);
}

function onMaskClick() {
  handleCancel();
}

// 确认中不允许 Escape 关闭，否则异步动作还在跑用户已经看不到弹窗了。
useDialogFocus(() => dialogRef.value, toRef(props, "visible"), {
  onEscape: () => {
    if (!props.confirmLoading) {
      handleCancel();
    }
  },
});

const busy = computed(() => props.confirmLoading);
</script>

<template>
  <Teleport to="body">
    <Transition name="mo-mask">
      <div
        v-show="visible"
        class="fixed inset-0 z-999 flex items-center justify-center bg-black/50 p-4"
        @click.self="onMaskClick"
      >
        <Transition name="mo-dialog">
          <div
            v-show="visible"
            ref="dialogRef"
            role="dialog"
            aria-modal="true"
            :aria-labelledby="titleID"
            :aria-busy="busy || undefined"
            class="relative z-10 w-full max-w-[360px] overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="rounded-[7px] bg-[#292929] p-5">
              <h3 :id="titleID" class="mb-3 text-base font-medium text-white">
                {{ title }}
              </h3>
              <p class="mb-5 max-h-[55vh] overflow-y-auto whitespace-pre-wrap text-sm leading-relaxed text-[#a3a3a3]">
                {{ content }}
              </p>
              <div class="flex justify-end gap-2">
                <Button v-if="showCancel" variant="default" :disabled="busy" @click="handleCancel">{{ cancelText }}</Button>
                <Button
                  variant="primary"
                  :disabled="confirmDisabled"
                  :loading="confirmLoading"
                  @click="handleConfirm"
                >{{ confirmText }}</Button>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>
