<script setup>
import Button from "@/components/ui/Button.vue";
import { useDialogFocus } from "@/composables/useDialogFocus";
import { ref, toRef } from "vue";

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: "提示" },
  content: { type: String, default: "" },
  placeholder: { type: String, default: "" },
  modelValue: { type: String, default: "" },
});

const emit = defineEmits(["update:visible", "update:modelValue", "confirm", "cancel"]);

const dialogRef = ref(null);
const inputRef = ref(null);
const titleID = `mo-input-title-${Math.random().toString(36).slice(2, 9)}`;

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

function onInput(event) {
  emit("update:modelValue", event?.target?.value ?? "");
}

function onEnter(event) {
  event.preventDefault();
  handleConfirm();
}

// 直接把初始焦点落在输入框上：回车确认的快捷键之前必须先用鼠标点一下输入框才生效。
useDialogFocus(() => dialogRef.value, toRef(props, "visible"), {
  onEscape: handleCancel,
  initialFocus: () => inputRef.value,
});
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
            class="relative z-10 w-full max-w-[380px] overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="rounded-[7px] bg-[#292929] p-5">
              <h3 :id="titleID" class="mb-3 text-base font-medium text-white">
                {{ title }}
              </h3>
              <p class="mb-3 text-sm leading-relaxed text-[#a3a3a3]">
                {{ content }}
              </p>
              <input
                ref="inputRef"
                :value="modelValue"
                :placeholder="placeholder"
                type="text"
                class="mb-5 h-9 w-full rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]"
                @input="onInput"
                @keydown.enter="onEnter"
              />
              <div class="flex justify-end gap-2">
                <Button variant="default" @click="handleCancel">取消</Button>
                <Button variant="primary" @click="handleConfirm">确定</Button>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>
