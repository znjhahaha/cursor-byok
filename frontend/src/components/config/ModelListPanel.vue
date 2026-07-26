<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import {
  appState,
  createEmptyModelAdapter,
  deleteModelAdapterAt,
  duplicateModelAdapterAt,
  findProviderByID,
  getModelAdapterTestResultByID,
  openModelEditorWindow,
  runModelAdapterTest,
  startModelAdapterTest,
  toUserError,
} from "@/state/appState";
import { computed, onBeforeUnmount, ref, watch } from "vue";

const emit = defineEmits(["error"]);

const BATCH_TEST_CONCURRENCY = 10;

const typeTabs = [
  { label: "OpenAI", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "Anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

const activeType = ref("openai");
const activeProviderID = ref("");
const batchTesting = ref(false);
const batchStopping = ref(false);
const batchTotal = ref(0);
const batchCompleted = ref(0);
const batchActiveCalls = new Set();
let batchStopRequested = false;

// 站点筛选只列出「确实有模型」的中转站，避免空站点堆满筛选条。
const providerFilters = computed(() => {
  const counts = new Map();
  for (const adapter of appState.modelAdapters) {
    if (adapter.type !== activeType.value || !adapter.providerID) {
      continue;
    }
    counts.set(adapter.providerID, (counts.get(adapter.providerID) ?? 0) + 1);
  }
  return appState.providers
    .filter((provider) => counts.has(provider.id))
    .map((provider) => ({ id: provider.id, name: provider.name, count: counts.get(provider.id) }));
});

const filteredAdapters = computed(() =>
  appState.modelAdapters.filter((adapter) => {
    if (adapter.type !== activeType.value) {
      return false;
    }
    if (!activeProviderID.value) {
      return true;
    }
    if (activeProviderID.value === "__local__") {
      return !adapter.providerID;
    }
    return adapter.providerID === activeProviderID.value;
  }),
);

const hasLocalAdapters = computed(() =>
  appState.modelAdapters.some((adapter) => adapter.type === activeType.value && !adapter.providerID),
);

const batchButtonText = computed(() => {
  if (batchStopping.value) {
    return "停止中...";
  }
  if (!batchTesting.value) {
    return "测试全部";
  }
  return `停止测试 ${batchCompleted.value}/${batchTotal.value}`;
});

watch(
  () => appState.modelAdapters,
  (adapters) => {
    if (adapters.some((adapter) => adapter.type === activeType.value)) {
      return;
    }
    const fallback = typeTabs.find((tab) => adapters.some((adapter) => adapter.type === tab.value));
    activeType.value = fallback?.value ?? "openai";
  },
  { deep: true, immediate: true },
);

// 切换协议或站点被删除后，筛选条件可能已失效，回落到「全部」而不是显示空列表。
watch([activeType, providerFilters, hasLocalAdapters], () => {
  if (!activeProviderID.value) {
    return;
  }
  if (activeProviderID.value === "__local__") {
    if (!hasLocalAdapters.value) {
      activeProviderID.value = "";
    }
    return;
  }
  if (!providerFilters.value.some((item) => item.id === activeProviderID.value)) {
    activeProviderID.value = "";
  }
});

function reportError(title, error) {
  emit("error", { title, message: String(error || "服务错误").trim() || "服务错误" });
}

function maskSecret(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "-";
  }
  if (text.length <= 8) {
    return `${"*".repeat(Math.max(text.length - 2, 0))}${text.slice(-2)}`;
  }
  return `${text.slice(0, 4)}****${text.slice(-4)}`;
}

function typeLabel(type) {
  return type === "anthropic" ? "Anthropic" : "OpenAI";
}

function providerLabel(adapter) {
  if (!adapter?.providerID) {
    return "";
  }
  return findProviderByID(appState.providers, adapter.providerID)?.name ?? "已失效";
}

function formatHost(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "-";
  }
  try {
    return new URL(text).host || text;
  } catch {
    return text.replace(/^https?:\/\//, "");
  }
}

async function openEditor(index = -1) {
  const adapter = index >= 0
    ? appState.modelAdapters[index]
    : { ...createEmptyModelAdapter(), type: activeType.value };
  try {
    await openModelEditorWindow(index, adapter);
  } catch (error) {
    reportError("打开失败", toUserError(error));
  }
}

async function handleDeleteModelAdapter(index) {
  if (!appState.modelAdapters[index]) {
    reportError("删除失败", "模型配置不存在，无法删除");
    return;
  }
  const result = await deleteModelAdapterAt(index);
  if (!result.ok) {
    reportError("删除失败", result.error);
  }
}

async function handleDuplicateModelAdapter(index) {
  if (!appState.modelAdapters[index]) {
    reportError("复制失败", "模型配置不存在，无法复制");
    return;
  }
  const result = await duplicateModelAdapterAt(index);
  if (!result.ok) {
    reportError("复制失败", result.error);
  }
}

function getAdapterTestResult(adapter) {
  return getModelAdapterTestResultByID(adapter?.id);
}

function isAdapterTesting(adapter) {
  return getAdapterTestResult(adapter)?.status === "running";
}

async function handleTestModelAdapter(adapter) {
  try {
    await runModelAdapterTest(adapter);
  } catch (_error) {
    // 失败结果会通过事件同步到界面，这里不再额外弹窗打断用户。
  }
}

function isCancelError(error) {
  return String(error?.name || "").trim() === "CancelError";
}

async function stopBatchTesting() {
  if (!batchTesting.value || batchStopping.value) {
    return;
  }
  batchStopRequested = true;
  batchStopping.value = true;
  const activeCalls = Array.from(batchActiveCalls);
  await Promise.allSettled(
    activeCalls.map((call) => (typeof call?.cancel === "function" ? call.cancel("batch-stop") : undefined)),
  );
}

async function handleTestAllModelAdapters() {
  if (batchTesting.value) {
    await stopBatchTesting();
    return;
  }
  const adapters = filteredAdapters.value.slice();
  if (adapters.length === 0) {
    return;
  }
  batchStopRequested = false;
  batchTesting.value = true;
  batchStopping.value = false;
  batchTotal.value = adapters.length;
  batchCompleted.value = 0;
  let nextIndex = 0;
  try {
    const workers = Array.from({ length: Math.min(BATCH_TEST_CONCURRENCY, adapters.length) }, async () => {
      while (!batchStopRequested) {
        const currentIndex = nextIndex;
        nextIndex += 1;
        if (currentIndex >= adapters.length) {
          return;
        }
        const call = startModelAdapterTest(adapters[currentIndex]);
        batchActiveCalls.add(call);
        try {
          await call;
        } catch (error) {
          if (!isCancelError(error) && !batchStopRequested) {
            // 单个失败结果由卡片自行展示，这里继续后续测试。
          }
        } finally {
          batchActiveCalls.delete(call);
          batchCompleted.value += 1;
        }
      }
    });
    await Promise.allSettled(workers);
  } finally {
    batchActiveCalls.clear();
    batchStopRequested = false;
    batchTesting.value = false;
    batchStopping.value = false;
  }
}

onBeforeUnmount(() => {
  void stopBatchTesting();
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="shrink-0 pb-4">
      <div class="flex items-center justify-between gap-4">
        <div class="center-row gap-2">
          <button
            v-for="tab in typeTabs"
            :key="tab.value"
            type="button"
            class="center-row gap-2 rounded-[8px] border px-3 py-2 text-sm transition-colors duration-150"
            :class="activeType === tab.value
              ? 'border-[#1ca35a] bg-[#123322] text-white'
              : 'border-[#343434] bg-[#252525] text-[#a3a3a3] hover:border-[#4a4a4a] hover:text-[#e5e5e5]'"
            @click="activeType = tab.value"
          >
            <span :class="[tab.icon, 'text-[16px]']"></span>
            <span>{{ tab.label }}</span>
          </button>
        </div>
        <div class="center-row gap-2">
          <Button
            variant="default"
            :disabled="appState.configSaving || (!batchTesting && filteredAdapters.length === 0)"
            @click="handleTestAllModelAdapters"
          >
            {{ batchButtonText }}
          </Button>
          <Button variant="primary" :disabled="appState.configSaving || batchTesting" @click="openEditor()">新增模型</Button>
        </div>
      </div>

      <div
        v-if="providerFilters.length > 0 || hasLocalAdapters"
        class="center-row mt-3 flex-wrap justify-start gap-1.5"
      >
        <button
          type="button"
          class="rounded-[999px] border px-2.5 py-1 text-xs transition-colors"
          :class="activeProviderID === ''
            ? 'border-[#1ca35a] bg-[#123322] text-white'
            : 'border-[#3a3a3a] bg-[#232323] text-[#a3a3a3] hover:text-[#e5e5e5]'"
          @click="activeProviderID = ''"
        >
          全部
        </button>
        <button
          v-for="item in providerFilters"
          :key="item.id"
          type="button"
          class="rounded-[999px] border px-2.5 py-1 text-xs transition-colors"
          :class="activeProviderID === item.id
            ? 'border-[#1ca35a] bg-[#123322] text-white'
            : 'border-[#3a3a3a] bg-[#232323] text-[#a3a3a3] hover:text-[#e5e5e5]'"
          @click="activeProviderID = item.id"
        >
          {{ item.name }} · {{ item.count }}
        </button>
        <button
          v-if="hasLocalAdapters"
          type="button"
          class="rounded-[999px] border px-2.5 py-1 text-xs transition-colors"
          :class="activeProviderID === '__local__'
            ? 'border-[#1ca35a] bg-[#123322] text-white'
            : 'border-[#3a3a3a] bg-[#232323] text-[#a3a3a3] hover:text-[#e5e5e5]'"
          @click="activeProviderID = '__local__'"
        >
          独立配置
        </button>
      </div>
    </div>

    <div class="min-h-0 flex-1">
      <div
        v-if="filteredAdapters.length === 0"
        class="flex h-full min-h-[220px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]"
      >
        当前还没有配置任何 {{ typeLabel(activeType) }} 模型。
      </div>

      <div v-else class="h-full min-h-0 overflow-y-auto pr-1">
        <div class="grid gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(250px,1fr))]">
          <Card
            v-for="(adapter, index) in filteredAdapters"
            :key="adapter.id || `${adapter.baseURL}-${adapter.modelID}-${index}`"
          >
            <div class="flex h-full min-h-[154px] flex-col justify-between gap-3">
              <div class="flex flex-col gap-2.5">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0 flex-1">
                    <div class="truncate text-base font-medium text-white">{{ adapter.displayName }}</div>
                    <div class="mt-1 truncate text-sm text-[#8f8f8f]">{{ adapter.modelID }}</div>
                    <div v-if="adapter.type === 'openai'" class="mt-0.5 truncate text-xs text-[#737373]">
                      {{ adapter.openAIEndpoint || "/v1/responses" }}
                    </div>
                  </div>
                  <span
                    class="center-row shrink-0 gap-1 rounded-[999px] border border-[#3f3f3f] px-[7px] py-[4px] text-[11px] font-medium text-[#cfcfcf]"
                  >
                    <span class="icon-[bxl--openai] text-[14px] !text-white" v-if="adapter.type === 'openai'"></span>
                    <span class="icon-[logos--claude-icon] text-[14px]" v-else></span>
                    <span>{{ typeLabel(adapter.type) }}</span>
                  </span>
                </div>

                <div class="grid grid-cols-2 gap-2 text-sm text-[#a3a3a3]">
                  <div class="rounded-[8px] bg-[#232323] px-3 py-2">
                    <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">
                      {{ providerLabel(adapter) ? "中转站" : "Host" }}
                    </div>
                    <div class="mt-1 truncate text-[#d4d4d4]" :title="adapter.baseURL">
                      {{ providerLabel(adapter) || formatHost(adapter.baseURL) }}
                    </div>
                  </div>
                  <div class="rounded-[8px] bg-[#232323] px-3 py-2">
                    <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">API Key</div>
                    <div class="mt-1 truncate text-[#d4d4d4]">{{ maskSecret(adapter.apiKey) }}</div>
                  </div>
                </div>

                <ModelAdapterTestCard
                  compact
                  title="测试"
                  empty-text="未测试"
                  :result="getAdapterTestResult(adapter)"
                />
              </div>

              <div class="center-row flex-wrap justify-end gap-2 border-t border-[#343434] pt-3">
                <Button
                  variant="default"
                  :disabled="appState.configSaving || batchTesting || isAdapterTesting(adapter)"
                  @click="handleTestModelAdapter(adapter)"
                >
                  {{ isAdapterTesting(adapter) ? "测试中..." : "测试" }}
                </Button>
                <Button variant="default" :disabled="appState.configSaving" @click="openEditor(appState.modelAdapters.indexOf(adapter))">编辑</Button>
                <Button variant="default" :disabled="appState.configSaving" @click="handleDuplicateModelAdapter(appState.modelAdapters.indexOf(adapter))">复制</Button>
                <Button variant="text" :disabled="appState.configSaving"
                  @click="handleDeleteModelAdapter(appState.modelAdapters.indexOf(adapter))">删除</Button>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </div>
  </div>
</template>