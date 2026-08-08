<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import Sortable from "sortablejs";
import {
  appState,
  cancelModelAdapterTest,
  createEmptyModelAdapter,
  deleteModelAdapterAt,
  duplicateModelAdapterAt,
  findProviderByID,
  getModelAdapterTestResultByID,
  openModelEditorWindow,
  runModelAdapterTest,
  saveModelAdapterOrder,
  startModelAdapterTest,
  toUserError,
} from "@/state/appState";
import { computed, nextTick, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";

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
const listScroller = ref(null);
const sortSaving = ref(false);
let batchStopRequested = false;
let sortable = null;

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

function isWarmupAdapter(adapter) {
  return Boolean(findProviderByID(appState.providers, adapter?.providerID)?.warmupEnabled);
}

async function handleCancelModelAdapter(adapter) {
  try {
    await cancelModelAdapterTest(adapter);
  } catch (error) {
    reportError("取消失败", toUserError(error));
  }
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
  const activeAdapters = filteredAdapters.value.filter((adapter) => isAdapterTesting(adapter) && isWarmupAdapter(adapter));
  await Promise.allSettled(
    [
      ...activeCalls.map((call) => (typeof call?.cancel === "function" ? call.cancel("batch-stop") : undefined)),
      ...activeAdapters.map((adapter) => cancelModelAdapterTest(adapter)),
    ],
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
    const concurrency = adapters.some((adapter) => isWarmupAdapter(adapter)) ? 2 : BATCH_TEST_CONCURRENCY;
    const workers = Array.from({ length: Math.min(concurrency, adapters.length) }, async () => {
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

// sortablejs 会直接改 DOM，而列表外层是 TransitionGroup（FLIP）。
// 两者同时改同一批节点会互相打架，所以 drop 后立刻用 revert 把 DOM 交还给 Vue，
// 真正的重排交给数据驱动，视觉过渡仍由 TransitionGroup 负责。
function destroySortable() {
  if (!sortable) {
    return;
  }
  sortable.destroy();
  sortable = null;
}

function syncSortable() {
  const element = listScroller.value?.querySelector("[data-model-grid]");
  if (!(element instanceof HTMLElement)) {
    destroySortable();
    return;
  }
  if (!sortable || sortable.el !== element) {
    destroySortable();
    sortable = Sortable.create(element, {
      animation: 160,
      draggable: ".model-sort-item",
      handle: ".model-sort-handle",
      ghostClass: "opacity-40",
      chosenClass: "!border-[#10AD5D]",
      dragClass: "cursor-grabbing",
      onEnd: (event) => {
        void handleModelSort(event);
      },
    });
  }
  sortable.option("disabled", sortSaving.value || appState.configSaving || batchTesting.value);
}

// 拖拽只发生在「当前可见子集」内，但 sort 是全量数组的连续序号。
// 因此把重排后的可见序列按原有槽位回填：不可见的模型一律留在原位置不动。
function mergeReorderedIntoAll(reorderedVisible) {
  const visibleIDs = new Set(filteredAdapters.value.map((adapter) => adapter.id));
  let cursor = 0;
  return appState.modelAdapters.map((adapter) => {
    if (!visibleIDs.has(adapter.id)) {
      return adapter;
    }
    const next = reorderedVisible[cursor];
    cursor += 1;
    return next;
  });
}

// 把被拖动的节点放回它原来的兄弟位置。sortablejs 的 onEnd 提供了
// oldIndex 对应的原始 DOM 顺序信息，这里用 draggable 集合重新定位。
function revertDraggedNode(event) {
  const item = event.item;
  const container = event.from;
  if (!(item instanceof HTMLElement) || !(container instanceof HTMLElement)) {
    return;
  }
  const oldIndex = event.oldDraggableIndex ?? event.oldIndex;
  if (!Number.isInteger(oldIndex)) {
    return;
  }
  const siblings = Array.from(container.querySelectorAll(":scope > .model-sort-item"))
    .filter((node) => node !== item);
  const anchor = siblings[oldIndex] ?? null;
  container.insertBefore(item, anchor);
}

async function handleModelSort(event) {
  const oldIndex = event.oldDraggableIndex ?? event.oldIndex;
  const newIndex = event.newDraggableIndex ?? event.newIndex;
  // sortablejs 已经把节点挪到新位置了。这里先把它塞回原来的兄弟节点之前，
  // 让 DOM 回到 Vue 认知中的状态，避免和随后的 FLIP 补丁互相错位。
  revertDraggedNode(event);
  if (!Number.isInteger(oldIndex) || !Number.isInteger(newIndex) || oldIndex === newIndex) {
    return;
  }

  const reorderedVisible = filteredAdapters.value.slice();
  const [movedAdapter] = reorderedVisible.splice(oldIndex, 1);
  if (!movedAdapter || newIndex < 0 || newIndex > reorderedVisible.length) {
    return;
  }
  reorderedVisible.splice(newIndex, 0, movedAdapter);

  const previousAdapters = appState.modelAdapters.slice();
  const nextAdapters = mergeReorderedIntoAll(reorderedVisible)
    .map((adapter, index) => ({ ...adapter, sort: index + 1 }));

  sortSaving.value = true;
  appState.modelAdapters = nextAdapters;
  try {
    const result = await saveModelAdapterOrder(nextAdapters.map((adapter) => adapter.id));
    if (!result.ok) {
      appState.modelAdapters = previousAdapters;
      reportError("排序失败", result.error);
    }
  } catch (error) {
    appState.modelAdapters = previousAdapters;
    reportError("排序失败", toUserError(error));
  } finally {
    sortSaving.value = false;
    await nextTick();
    syncSortable();
  }
}

watch(
  [filteredAdapters, sortSaving, batchTesting, () => appState.configSaving],
  () => {
    void nextTick().then(syncSortable);
  },
  { flush: "post" },
);

onMounted(() => {
  void nextTick().then(syncSortable);
});

onBeforeUnmount(() => {
  void stopBatchTesting();
  destroySortable();
});
// KeepAlive 下切 tab 不触发 unmount。批量测速是 10 并发、且会持续改写 appState，
// 留一个用户看不见也停不掉的后台任务比丢进度更糟，所以切走时同样停掉。
onDeactivated(() => {
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
            :disabled="sortSaving || appState.configSaving || (!batchTesting && filteredAdapters.length === 0)"
            :loading="batchStopping"
            @click="handleTestAllModelAdapters"
          >
            {{ batchButtonText }}
          </Button>
          <Button variant="primary" :disabled="sortSaving || appState.configSaving || batchTesting" @click="openEditor()">新增模型</Button>
        </div>
      </div>

      <!-- batchCompleted / batchTotal 一直算着，之前只体现在按钮文案里。
           10 并发的批处理值得一条真进度条；纯视觉，不新增任何文案。 -->
      <div
        v-if="batchTesting && batchTotal > 0"
        class="mt-3 h-[2px] overflow-hidden rounded-full bg-[#2c2c2c]"
        role="progressbar"
        :aria-valuenow="batchCompleted"
        :aria-valuemin="0"
        :aria-valuemax="batchTotal"
      >
        <div
          class="h-full rounded-full bg-[#10AD5D] transition-[width] duration-enter ease-out"
          :style="{ width: `${Math.round((batchCompleted / batchTotal) * 100)}%` }"
        ></div>
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

      <div v-else ref="listScroller" class="h-full min-h-0 overflow-y-auto pr-1">
        <!-- 卡片数量在几十以内，FLIP 重排负担得起。
             复制模型时会凭空多出一张卡，没有进场动画的话用户看不出是哪张。 -->
        <TransitionGroup
          tag="div"
          name="mo-list"
          data-model-grid
          class="relative grid gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(250px,1fr))]"
        >
          <Card
            v-for="(adapter, index) in filteredAdapters"
            :key="adapter.id || `${adapter.baseURL}-${adapter.modelID}-${index}`"
            class="model-sort-item group relative h-full [&>div]:h-full"
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
                  @cancel="handleCancelModelAdapter(adapter)"
                />
              </div>

              <div class="center-row flex-wrap gap-2 border-t border-[#343434] pt-3">
                <button
                  type="button"
                  class="model-sort-handle center-row mr-auto h-[26px] w-[26px] shrink-0 touch-none cursor-grab justify-center rounded-[6px] border border-transparent bg-transparent text-[#5c5c5c] outline-none transition-[color,border-color,background-color] hover:border-[#454545] hover:bg-[#333333] hover:text-white focus-visible:border-[#10AD5D] focus-visible:bg-[#333333] focus-visible:text-white active:cursor-grabbing disabled:cursor-not-allowed disabled:opacity-30"
                  :disabled="sortSaving || appState.configSaving || batchTesting"
                  aria-label="拖拽排序"
                  title="拖拽排序"
                  @click.stop
                >
                  <span class="icon-[icon-park-outline--drag] text-[16px]"></span>
                </button>
                <Button
                  variant="default"
                  :disabled="sortSaving || appState.configSaving || batchTesting"
                  :loading="isAdapterTesting(adapter) && !getAdapterTestResult(adapter)?.warmupCancelable"
                  @click="isAdapterTesting(adapter) && getAdapterTestResult(adapter)?.warmupCancelable
                    ? handleCancelModelAdapter(adapter)
                    : handleTestModelAdapter(adapter)"
                >
                  {{ isAdapterTesting(adapter)
                    ? (getAdapterTestResult(adapter)?.warmupCancelable ? "取消排队" : "测试中...")
                    : (isWarmupAdapter(adapter) ? "排队检测" : "测试") }}
                </Button>
                <Button variant="default" :disabled="sortSaving || appState.configSaving" @click="openEditor(appState.modelAdapters.indexOf(adapter))">编辑</Button>
                <Button variant="default" :disabled="sortSaving || appState.configSaving" @click="handleDuplicateModelAdapter(appState.modelAdapters.indexOf(adapter))">复制</Button>
                <Button variant="text" :disabled="sortSaving || appState.configSaving"
                  @click="handleDeleteModelAdapter(appState.modelAdapters.indexOf(adapter))">删除</Button>
              </div>
            </div>
          </Card>
        </TransitionGroup>
      </div>
    </div>
  </div>
</template>
