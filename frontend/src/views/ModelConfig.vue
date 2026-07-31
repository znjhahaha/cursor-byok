<script setup>
import ModelListPanel from "@/components/config/ModelListPanel.vue";
import ProviderListPanel from "@/components/config/ProviderListPanel.vue";
import UsageStatsPanel from "@/components/config/UsageStatsPanel.vue";
import DiagnosticsPanel from "@/components/config/DiagnosticsPanel.vue";
import { showModal } from "@/composables/useModal";
import { reloadUserConfig } from "@/state/appState";
import { Events } from "@wailsio/runtime";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

const mainTabs = [
  { label: "中转站", value: "providers", icon: "icon-[mdi--server-network]" },
  { label: "模型配置", value: "models", icon: "icon-[mdi--api]" },
  { label: "调用统计", value: "stats", icon: "icon-[mdi--chart-bar]" },
  { label: "日志与诊断", value: "diagnostics", icon: "icon-[mdi--text-box-search-outline]" },
];

const activeTab = ref("providers");

const PANEL_COMPONENTS = {
  providers: ProviderListPanel,
  models: ModelListPanel,
  stats: UsageStatsPanel,
  diagnostics: DiagnosticsPanel,
};

const activePanel = computed(() => PANEL_COMPONENTS[activeTab.value] ?? ProviderListPanel);

// 首页的「调用统计」入口通过 storage 传递落地 tab，读取后立即清除，
// 避免用户下次从常规入口进来时被粘住在统计页。
function consumeInitialTab() {
  try {
    const stored = window.localStorage.getItem("modelConfig:initialTab");
    window.localStorage.removeItem("modelConfig:initialTab");
    if (mainTabs.some((tab) => tab.value === stored)) {
      activeTab.value = stored;
    }
  } catch (_error) {
    // storage 不可用时保持默认 tab。
  }
}

async function handlePanelError({ title, message }) {
  await showModal({ title, content: message });
}

async function handleConfirmDelete({ provider, modelCount, run }) {
  const content = modelCount > 0
    ? `删除「${provider.name}」将同时移除其下的 ${modelCount} 个模型配置，确定继续？`
    : `确定删除中转站「${provider.name}」？`;
  const confirmed = await showModal({ title: "删除中转站", content });
  if (!confirmed) {
    return;
  }
  const result = await run();
  if (!result.ok) {
    await showModal({ title: "删除失败", content: result.error });
  }
}

// 窗口已打开时，首页入口通过广播直接指定落地 tab（新窗口首次打开仍走上面的 consumeInitialTab）。
const MODEL_CONFIG_TAB_EVENT = "modelConfig:activateTab";
let unsubscribeActivateTab = null;

onMounted(async () => {
  consumeInitialTab();
  unsubscribeActivateTab = Events.On(MODEL_CONFIG_TAB_EVENT, (event) => {
    const tab = typeof event?.data === "string" ? event.data : "";
    if (mainTabs.some((item) => item.value === tab)) {
      activeTab.value = tab;
    }
  });
  await reloadUserConfig({ modelAdaptersOnly: true }).catch(() => { });
});

onBeforeUnmount(() => {
  unsubscribeActivateTab?.();
  unsubscribeActivateTab = null;
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col overflow-hidden p-4 pt-0 text-[#e5e5e5]">
    <div class="center-row shrink-0 justify-start gap-2 pb-4">
      <button
        v-for="tab in mainTabs"
        :key="tab.value"
        type="button"
        class="center-row gap-2 rounded-[8px] border px-3 py-2 text-sm transition-colors duration-150"
        :class="activeTab === tab.value
          ? 'border-[#1ca35a] bg-[#123322] text-white'
          : 'border-[#343434] bg-[#252525] text-[#a3a3a3] hover:border-[#4a4a4a] hover:text-[#e5e5e5]'"
        @click="activeTab = tab.value"
      >
        <span :class="[tab.icon, 'text-[16px]']"></span>
        <span>{{ tab.label }}</span>
      </button>
    </div>

    <!--
      KeepAlive 让三个面板切走再切回时保留筛选条件、展开状态与滚动位置，
      UsageStatsPanel 也不再每次进入都重发一轮 IPC。
      代价是切 tab 不再触发 onBeforeUnmount，所以三个面板各自补了 onDeactivated 清理。

      过渡用重叠式（mo-panel + .mo-swap）而不是 mode="out-in"：
      面板高度是 flex-1 的未知值，两个同时在文档流里会各占满高度、闪出双滚动条；
      而 out-in 会把 60ms 错开量叠在 160ms 离场之后，总时长拉到 420ms。
    -->
    <div class="mo-swap min-h-0 flex-1">
      <Transition name="mo-panel">
        <KeepAlive>
          <component
            :is="activePanel"
            :key="activeTab"
            @error="handlePanelError"
            @confirm-delete="handleConfirmDelete"
          />
        </KeepAlive>
      </Transition>
    </div>
  </div>
</template>
