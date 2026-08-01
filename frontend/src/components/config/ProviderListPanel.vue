<script setup>
import ProviderModelPicker from "@/components/config/ProviderModelPicker.vue";
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import Switch from "@/components/ui/Switch.vue";
import {
  appState,
  cloneProvider,
  countProviderModels,
  deleteProvider,
  deleteProviderModels,
  fetchProviderModels,
  formatImportSummary,
  hasCachedProviderModels,
  importAllProviderModels,
  openProviderEditorWindow,
  renameProviderModels,
  setProviderDisabled,
  setProviderPinned,
  toUserError,
} from "@/state/appState";
import dayjs from "dayjs";
import { computed, onBeforeUnmount, onDeactivated, ref } from "vue";

const emit = defineEmits(["error", "confirm-delete", "confirm-action"]);

const PROVIDER_SORT_DEFAULT = "default";
const PROVIDER_SORT_NAME = "name";
const PROVIDER_SORT_MODELS = "models";
const PROVIDER_SORT_FETCHED = "fetched";

const SORT_OPTIONS = [
  { label: "默认顺序", value: PROVIDER_SORT_DEFAULT, icon: "icon-[mdi--sort-variant]" },
  { label: "按名称", value: PROVIDER_SORT_NAME, icon: "icon-[mdi--sort-alphabetical-ascending]" },
  { label: "按模型数", value: PROVIDER_SORT_MODELS, icon: "icon-[mdi--sort-numeric-descending]" },
  { label: "按最近获取", value: PROVIDER_SORT_FETCHED, icon: "icon-[mdi--clock-outline]" },
];

const ACTION_CLONE = "clone";
const ACTION_IMPORT_ALL = "import-all";
const ACTION_CLEAR_MODELS = "clear-models";
const ACTION_RENAME_MODELS = "rename-models";
const ACTION_DELETE = "delete";

// 删除放在菜单末尾并标记 danger：它原本与「获取模型」同级摆在操作行里，
// 而克隆/清空这些危险度更低的操作却藏在二级菜单，层级与危险度是倒挂的。
const ACTION_OPTIONS = [
  { label: "克隆站点", value: ACTION_CLONE, icon: "icon-[mdi--content-duplicate]" },
  { label: "导入全部模型", value: ACTION_IMPORT_ALL, icon: "icon-[mdi--download-multiple]" },
  { label: "按模板重命名模型", value: ACTION_RENAME_MODELS, icon: "icon-[mdi--rename-outline]" },
  { label: "清空该站模型", value: ACTION_CLEAR_MODELS, icon: "icon-[mdi--playlist-remove]" },
  { label: "删除站点", value: ACTION_DELETE, icon: "icon-[mdi--trash-can-outline]", danger: true },
];

// 同时探测多个站点时限制并发：单次探测最坏要串行试 7 个各带 8s 超时的路径，
// 放开并发会把本机连接数和中转站限流一起打满。
const BATCH_PROBE_CONCURRENCY = 4;

// 探活状态只存在于本次会话中，不落盘：它反映的是「此刻站点是否可达」。
const probing = ref(new Set());

// 单值控制：同时只展开一个卡片，避免多张列表同时铺开把页面撑爆。
// 卡片原地展开（不再跨列），所以这一个 ref 就是完整的展开状态 ——
// 展开与收起互斥、只展开一张，都由「赋值」本身表达。
const expandedID = ref("");
const probingPath = ref({});
const refreshSucceeded = ref(new Set());
const importHint = ref({ id: "", text: "" });
const keyword = ref("");
const sortKey = ref(PROVIDER_SORT_DEFAULT);
const togglingID = ref("");
const batchProbing = ref(false);
const batchStopping = ref(false);
const batchTotal = ref(0);
const batchCompleted = ref(0);
let batchStopRequested = false;
let hintTimer = null;
const probeTimers = new Map();
const refreshFeedbackTimers = new Map();

const decoratedProviders = computed(() =>
  appState.providers.map((provider) => ({
    ...provider,
    modelCount: countProviderModels(provider.id),
    catalog: appState.providerModelCatalog[provider.id] ?? null,
    probing: probing.value.has(provider.id),
    probingPath: probingPath.value[provider.id] || "",
    refreshSucceeded: refreshSucceeded.value.has(provider.id),
    expanded: expandedID.value === provider.id,
    toggling: togglingID.value === provider.id,
  })),
);

// 搜索与排序都是会话态，不落盘；只有置顶是用户对这份配置的长期意图。
// 置顶永远优先于排序键，否则「置顶」在切换排序后就失去意义。
const providers = computed(() => {
  const text = keyword.value.trim().toLowerCase();
  const filtered = text
    ? decoratedProviders.value.filter((provider) =>
      [provider.name, provider.baseURL, provider.note].some((field) =>
        String(field || "").toLowerCase().includes(text)))
    : decoratedProviders.value;
  const indexOf = new Map(decoratedProviders.value.map((provider, index) => [provider.id, index]));
  const compare = {
    [PROVIDER_SORT_NAME]: (left, right) => left.name.localeCompare(right.name, "zh-Hans-CN"),
    [PROVIDER_SORT_MODELS]: (left, right) => right.modelCount - left.modelCount,
    [PROVIDER_SORT_FETCHED]: (left, right) => (right.catalog?.fetchedAt ?? 0) - (left.catalog?.fetchedAt ?? 0),
  }[sortKey.value] ?? (() => 0);
  return filtered.slice().sort((left, right) =>
    Number(Boolean(right.pinned)) - Number(Boolean(left.pinned))
    || compare(left, right)
    || (indexOf.get(left.id) ?? 0) - (indexOf.get(right.id) ?? 0));
});

const batchButtonText = computed(() => {
  if (batchStopping.value) {
    return "停止中...";
  }
  if (!batchProbing.value) {
    return "测试全部";
  }
  return `停止测试 ${batchCompleted.value}/${batchTotal.value}`;
});

function typeLabel(type) {
  return type === "anthropic" ? "Anthropic" : "OpenAI";
}

function typeIcon(type) {
  return type === "anthropic" ? "icon-[logos--claude-icon]" : "icon-[bxl--openai]";
}

function maskSecret(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "未配置";
  }
  if (text.length <= 8) {
    return `${"*".repeat(Math.max(text.length - 2, 0))}${text.slice(-2)}`;
  }
  return `${text.slice(0, 4)}****${text.slice(-4)}`;
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

function probeText(provider) {
  if (provider.probing) {
    return provider.probingPath ? `正在尝试 ${provider.probingPath}` : "正在探测 API 路径...";
  }
  if (!provider.catalog) {
    return "未获取";
  }
  if (provider.catalog.ok) {
    return `可用 · ${provider.catalog.models.length} 个模型`;
  }
  if (provider.catalog.reachable) {
    return "接口可达 · 需要手动添加模型";
  }
  return provider.catalog.error || "不可用";
}

function probeClass(provider) {
  if (provider.probing || !provider.catalog) {
    return "text-[#a3a3a3]";
  }
  return provider.catalog.ok || provider.catalog.reachable ? "text-[#86efac]" : "text-[#fca5a5]";
}

function reportError(title, error) {
  emit("error", { title, message: String(error || "服务错误").trim() || "服务错误" });
}

async function openEditor(provider = null) {
  try {
    await openProviderEditorWindow(provider);
  } catch (error) {
    reportError("打开失败", toUserError(error));
  }
}

// 路径轮播只是给多秒级探测填等待感的假进度。命中缓存时整个调用是瞬时的，
// 这时候再播一遍「正在尝试 /v1/models」反而显得假，所以只在真会走网络时才启动。
function startProbeIndicator(providerID) {
  const candidates = ["/v1/models", "/models", "/api/v1/models", "模型请求接口"];
  let index = 0;
  probingPath.value = { ...probingPath.value, [providerID]: candidates[index] };
  probeTimers.set(
    providerID,
    window.setInterval(() => {
      index = Math.min(index + 1, candidates.length - 1);
      probingPath.value = { ...probingPath.value, [providerID]: candidates[index] };
    }, 1100),
  );
}

async function handleProbe(provider, { force = false } = {}) {
  if (probing.value.has(provider.id)) {
    return { ok: false, busy: true, error: "模型列表正在刷新" };
  }
  const willHitCache = !force && hasCachedProviderModels(provider);
  probing.value = new Set(probing.value).add(provider.id);
  if (!willHitCache) {
    startProbeIndicator(provider.id);
  }
  try {
    return await fetchProviderModels(provider, { force });
  } catch (error) {
    const message = toUserError(error);
    return { ok: false, error: message };
  } finally {
    window.clearInterval(probeTimers.get(provider.id));
    probeTimers.delete(provider.id);
    const nextPaths = { ...probingPath.value };
    delete nextPaths[provider.id];
    probingPath.value = nextPaths;
    const next = new Set(probing.value);
    next.delete(provider.id);
    probing.value = next;
  }
}

function isUsableProbeOutcome(outcome) {
  return Boolean(outcome?.ok || (outcome?.result?.reachable && outcome?.provider));
}

function showRefreshSuccess(providerID) {
  window.clearTimeout(refreshFeedbackTimers.get(providerID));
  refreshSucceeded.value = new Set(refreshSucceeded.value).add(providerID);
  refreshFeedbackTimers.set(
    providerID,
    window.setTimeout(() => {
      const next = new Set(refreshSucceeded.value);
      next.delete(providerID);
      refreshSucceeded.value = next;
      refreshFeedbackTimers.delete(providerID);
    }, 1400),
  );
}

// 网络/鉴权失败不覆盖旧 catalog，但必须给出明确反馈；可达但无标准模型列表
// 属于有效探测结果，会切换到手动添加模型区域。
async function handleRefresh(provider) {
  window.clearTimeout(refreshFeedbackTimers.get(provider.id));
  refreshFeedbackTimers.delete(provider.id);
  const pendingFeedback = new Set(refreshSucceeded.value);
  pendingFeedback.delete(provider.id);
  refreshSucceeded.value = pendingFeedback;

  const outcome = await handleProbe(provider, { force: true });
  if (outcome?.busy) {
    return;
  }
  if (!isUsableProbeOutcome(outcome)) {
    reportError("刷新模型失败", outcome?.error);
    return;
  }
  showRefreshSuccess(provider.id);
}

function fetchedAtLabel(provider) {
  const fetchedAt = provider.catalog?.fetchedAt;
  if (!fetchedAt) {
    return "";
  }
  return dayjs(fetchedAt).format("MM-DD HH:mm");
}

function motionDurationMS(name, fallback) {
  if (typeof window === "undefined") {
    return fallback;
  }
  const raw = window.getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  if (!raw) {
    return fallback;
  }
  const parsed = raw.endsWith("ms")
    ? Number.parseFloat(raw)
    : Number.parseFloat(raw) * 1000;
  return Number.isFinite(parsed) ? parsed : fallback;
}

// 展开是纯粹的单值切换：卡片留在原列内长高，没有「先变宽再展开」的中间态，
// 于是也不需要状态机去协调两段动画的先后。
function closeExpanded() {
  expandedID.value = "";
}

function openExpanded(providerID) {
  expandedID.value = providerID;
}

// 高度动画显式测量，不用 grid-template-rows 的 0fr→1fr：
// 后者要求浏览器能记住过渡起始值，而这块内容是异步填充的，起始值不可靠时
// 整段过渡会被跳过。这里用 scrollHeight 固化起止值，靠强制 reflow 而不是猜帧数。
const COLLAPSE_EASING = "var(--mo-ease)";

function collapseTransition(el) {
  return `height ${motionDurationMS("--mo-panel", 220)}ms ${COLLAPSE_EASING}, opacity ${motionDurationMS("--mo-fast", 140)}ms ${COLLAPSE_EASING}`;
}

function collapseEnter(el, done) {
  el.style.overflow = "hidden";
  el.style.transition = "none";
  el.style.height = "0px";
  el.style.opacity = "0";
  void el.offsetHeight;
  el.style.transition = collapseTransition(el);
  el.style.height = `${el.scrollHeight}px`;
  el.style.opacity = "1";
  const finish = () => {
    el.removeEventListener("transitionend", onEnd);
    // 必须交还给 auto：模型列表是异步填充的，锁死像素值会把后到的内容截断。
    el.style.height = "";
    el.style.overflow = "";
    el.style.transition = "";
    done();
  };
  const onEnd = (event) => {
    if (event.target === el && event.propertyName === "height") {
      finish();
    }
  };
  el.addEventListener("transitionend", onEnd);
}

function collapseLeave(el, done) {
  el.style.overflow = "hidden";
  el.style.transition = "none";
  el.style.height = `${el.scrollHeight}px`;
  el.style.opacity = "1";
  void el.offsetHeight;
  el.style.transition = collapseTransition(el);
  el.style.height = "0px";
  el.style.opacity = "0";
  const finish = () => {
    el.removeEventListener("transitionend", onEnd);
    el.style.height = "";
    el.style.opacity = "";
    el.style.overflow = "";
    el.style.transition = "";
    done();
  };
  const onEnd = (event) => {
    if (event.target === el && event.propertyName === "height") {
      finish();
    }
  };
  el.addEventListener("transitionend", onEnd);
}

// 「获取模型」把探活与展开合并成一个动作：拉取本身就是最真实的可用性验证。
async function handleFetchAndExpand(provider) {
  if (provider.expanded) {
    closeExpanded();
    return;
  }
  importHint.value = { id: "", text: "" };
  openExpanded(provider.id);
  const outcome = await handleProbe(provider);
  if (!outcome?.busy && !isUsableProbeOutcome(outcome)) {
    reportError("获取模型失败", outcome?.error);
  }
}

function toggleExpand(provider) {
  if (!provider.catalog) {
    return;
  }
  if (provider.expanded) {
    closeExpanded();
  } else {
    openExpanded(provider.id);
  }
}

function handleImported(provider, summary) {
  window.clearTimeout(hintTimer);
  importHint.value = {
    id: provider.id,
    text: formatImportSummary(summary),
  };
  hintTimer = window.setTimeout(() => {
    importHint.value = { id: "", text: "" };
  }, 3000);
}

function showHint(providerID, text) {
  window.clearTimeout(hintTimer);
  importHint.value = { id: providerID, text };
  hintTimer = window.setTimeout(() => {
    importHint.value = { id: "", text: "" };
  }, 3000);
}

// 「测试全部」= 对每个站点跑一次强制刷新。拉取模型列表本身就是最真实的可用性验证，
// 所以这里不额外造一套探活协议，直接复用单站点那条路径。
function stopBatchProbe() {
  if (!batchProbing.value || batchStopping.value) {
    return;
  }
  batchStopRequested = true;
  batchStopping.value = true;
}

async function handleProbeAll() {
  if (batchProbing.value) {
    stopBatchProbe();
    return;
  }
  const targets = providers.value.filter((provider) => !provider.disabled);
  if (targets.length === 0) {
    return;
  }
  batchStopRequested = false;
  batchProbing.value = true;
  batchStopping.value = false;
  batchTotal.value = targets.length;
  batchCompleted.value = 0;
  let nextIndex = 0;
  const failed = [];
  try {
    const workers = Array.from({ length: Math.min(BATCH_PROBE_CONCURRENCY, targets.length) }, async () => {
      while (!batchStopRequested) {
        const currentIndex = nextIndex;
        nextIndex += 1;
        if (currentIndex >= targets.length) {
          return;
        }
        const provider = targets[currentIndex];
        const outcome = await handleProbe(provider, { force: true });
        if (!outcome?.busy && !isUsableProbeOutcome(outcome)) {
          failed.push(provider.name);
        }
        batchCompleted.value += 1;
      }
    });
    await Promise.allSettled(workers);
  } finally {
    batchStopRequested = false;
    batchProbing.value = false;
    batchStopping.value = false;
  }
  if (failed.length > 0) {
    reportError("测试全部中转站", `${failed.length} 个站点不可用：${failed.join("、")}`);
  }
}

async function handleToggleDisabled(provider, enabled) {
  togglingID.value = provider.id;
  try {
    const result = await setProviderDisabled(provider.id, !enabled);
    if (!result.ok) {
      reportError("切换失败", result.error);
    }
  } finally {
    togglingID.value = "";
  }
}

async function handleTogglePinned(provider) {
  const result = await setProviderPinned(provider.id, !provider.pinned);
  if (!result.ok) {
    reportError("操作失败", result.error);
  }
}

// 会改动已落盘模型集合的两项走确认弹窗；确认交互统一由父级持有，
// 面板本身不管弹窗状态。
function handleProviderAction(provider, action) {
  switch (action) {
    case ACTION_CLONE:
      return runClone(provider);
    case ACTION_IMPORT_ALL:
      return runImportAll(provider);
    case ACTION_RENAME_MODELS:
      return emit("confirm-action", {
        title: "按模板重命名模型",
        content: `将按站点模板重刷「${provider.name}」下 ${provider.modelCount} 个模型的显示名。显示名参与渠道标识，改名后 Cursor 里已选中的这些模型需要重新选择，测速与用量统计也会重新计数。`,
        failureTitle: "重命名失败",
        run: async () => {
          const result = await renameProviderModels(provider.id);
          if (result.ok) {
            showHint(provider.id, `已重命名 ${result.renamed ?? 0} 个模型`);
          }
          return result;
        },
      });
    case ACTION_CLEAR_MODELS:
      return emit("confirm-action", {
        title: "清空该站模型",
        content: `将删除「${provider.name}」下的 ${provider.modelCount} 个模型配置，中转站本身保留。`,
        failureTitle: "清空失败",
        run: async () => {
          const result = await deleteProviderModels(provider.id);
          if (result.ok) {
            showHint(provider.id, `已删除 ${result.removed ?? 0} 个模型`);
          }
          return result;
        },
      });
    case ACTION_DELETE:
      return handleDelete(provider);
    default:
      return undefined;
  }
}

async function runClone(provider) {
  const result = await cloneProvider(provider.id);
  if (!result.ok) {
    reportError("克隆失败", result.error);
    return;
  }
  showHint(provider.id, `已克隆为「${result.provider?.name ?? provider.name}」`);
}

// 导入全部依赖已拉取到的模型列表；没有列表时先补一次拉取，
// 免得用户对着「请先获取模型列表」反复点两个按钮。
async function runImportAll(provider) {
  if (!provider.catalog?.ok) {
    const outcome = await handleProbe(provider);
    if (!outcome?.busy && !isUsableProbeOutcome(outcome)) {
      reportError("获取模型失败", outcome?.error);
      return;
    }
  }
  const result = await importAllProviderModels(provider.id);
  if (!result.ok) {
    reportError("导入失败", result.error);
    return;
  }
  handleImported(provider, result);
}

function clearAllTimers() {
  window.clearTimeout(hintTimer);
  for (const timer of probeTimers.values()) {
    window.clearInterval(timer);
  }
  probeTimers.clear();
  for (const timer of refreshFeedbackTimers.values()) {
    window.clearTimeout(timer);
  }
  refreshFeedbackTimers.clear();
  refreshSucceeded.value = new Set();
}

onBeforeUnmount(clearAllTimers);
// 本面板被 ModelConfig 用 KeepAlive 缓存，切走时不会触发 unmount。
// 少了这一句，路径轮播的 setInterval 会在隐藏的面板上一直滴答，
// 批量探测也会在用户看不见的地方继续打站点。
onDeactivated(() => {
  clearAllTimers();
  stopBatchProbe();
});

// 删除必须级联，否则残留的悬空引用会让整份配置校验失败、用户再也存不进任何修改。
// 确认交互交给父级统一处理，面板本身不持有弹窗状态。
function handleDelete(provider) {
  emit("confirm-delete", {
    provider,
    modelCount: provider.modelCount,
    run: () => deleteProvider(provider.id),
  });
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="flex shrink-0 flex-col gap-3 pb-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="text-sm text-[#a3a3a3]">
          中转站提供接口地址与密钥，其下的模型自动继承，无需逐个填写。
        </div>
        <div class="center-row gap-2">
          <Button
            variant="default"
            :disabled="appState.configSaving || (!batchProbing && providers.length === 0)"
            :loading="batchStopping"
            @click="handleProbeAll"
          >
            {{ batchButtonText }}
          </Button>
          <Button variant="primary" :disabled="appState.configSaving" @click="openEditor()">新增中转站</Button>
        </div>
      </div>

      <div
        v-if="batchProbing && batchTotal > 0"
        class="h-[2px] overflow-hidden rounded-full bg-[#2c2c2c]"
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

      <div v-if="decoratedProviders.length > 1" class="center-row justify-start gap-2">
        <div class="relative min-w-0 flex-1 sm:max-w-[320px]">
          <span
            class="icon-[mdi--magnify] pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-[15px] text-[#737373]"
          ></span>
          <input
            v-model="keyword"
            type="text"
            spellcheck="false"
            placeholder="搜索名称或地址"
            class="h-8 w-full rounded-[6px] border border-[#3f3f3f] bg-[#232323] pl-7 pr-2 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]"
          />
        </div>
        <div class="w-[150px] shrink-0">
          <Select v-model="sortKey" :options="SORT_OPTIONS" aria-label="中转站排序" />
        </div>
      </div>
    </div>

    <div class="min-h-0 flex-1">
      <div
        v-if="providers.length === 0"
        class="flex h-full min-h-[220px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]"
      >
        {{ decoratedProviders.length === 0 ? "还没有可用的中转站。" : "没有匹配的中转站。" }}
      </div>

      <div v-else class="h-full min-h-0 overflow-y-auto pr-1">
        <!-- items-start 是必须的：grid 默认拉伸同行所有单元格，
             展开的卡片会把邻居一起顶到同样高度，留下大片空白。 -->
        <!-- 280px 在操作行收成单行后仍偏紧（最窄卡片要放下「获取模型」+3 个 icon 控件），
             放到 300px 留出余量。 -->
        <div
          class="grid items-start gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(300px,1fr))]"
        >
          <Card v-for="provider in providers" :key="provider.id">
            <div
              class="flex min-h-[168px] flex-col justify-between gap-3 transition-opacity"
              :class="provider.disabled ? 'opacity-55' : ''"
            >
              <div class="flex flex-col gap-2.5">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0 flex-1">
                    <div class="center-row justify-start gap-2">
                      <button
                        type="button"
                        class="shrink-0 rounded-[4px] p-[2px] transition-colors"
                        :class="provider.pinned ? 'text-[#10AD5D]' : 'text-[#5f5f5f] hover:text-[#a3a3a3]'"
                        :aria-label="provider.pinned ? `取消置顶 ${provider.name}` : `置顶 ${provider.name}`"
                        :title="provider.pinned ? '取消置顶' : '置顶'"
                        @click="handleTogglePinned(provider)"
                      >
                        <span
                          class="text-[15px]"
                          :class="provider.pinned ? 'icon-[mdi--pin]' : 'icon-[mdi--pin-outline]'"
                        ></span>
                      </button>
                      <span class="truncate text-base font-medium text-white">{{ provider.name }}</span>
                      <span
                        v-if="provider.builtin"
                        class="shrink-0 rounded-[999px] border border-[#3f3f3f] px-[6px] py-[2px] text-[10px] text-[#cfcfcf]"
                      >
                        内置
                      </span>
                    </div>
                    <div class="mt-1 truncate text-sm text-[#8f8f8f]" :title="provider.baseURL">
                      {{ formatHost(provider.baseURL) }}
                    </div>
                  </div>
                  <span
                    class="center-row shrink-0 gap-1 rounded-[999px] border border-[#3f3f3f] px-[7px] py-[4px] text-[11px] font-medium text-[#cfcfcf]"
                  >
                    <span :class="[typeIcon(provider.type), 'text-[14px]']"></span>
                    <span>{{ typeLabel(provider.type) }}</span>
                  </span>
                </div>

                <div class="grid grid-cols-2 gap-2 text-sm text-[#a3a3a3]">
                  <div class="rounded-[8px] bg-[#232323] px-3 py-2">
                    <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">API Key</div>
                    <div class="mt-1 truncate text-[#d4d4d4]">{{ maskSecret(provider.apiKey) }}</div>
                  </div>
                  <div class="rounded-[8px] bg-[#232323] px-3 py-2">
                    <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">已配模型</div>
                    <div class="mt-1 truncate" :class="provider.modelCount > 0 ? 'text-[#d4d4d4]' : 'text-[#737373]'">
                      {{ provider.modelCount > 0 ? provider.modelCount : "点击获取模型" }}
                    </div>
                  </div>
                </div>

                <button
                  type="button"
                  class="w-full rounded-[8px] bg-[#232323] px-3 py-2 text-left text-sm transition-colors duration-150"
                  :class="provider.catalog ? 'cursor-pointer hover:bg-[#2a2a2a]' : 'cursor-default'"
                  @click="toggleExpand(provider)"
                >
                  <div class="center-row justify-between gap-2">
                    <span class="text-[11px] uppercase tracking-[0.08em] text-[#666]">状态</span>
                    <span class="center-row shrink-0 gap-1.5">
                      <!-- 只显示数字时间戳，不加文案：新增中文字面量会带来三语补译成本。 -->
                      <span
                        v-if="fetchedAtLabel(provider) && !provider.probing"
                        class="font-num text-[11px] tabular-nums text-[#666]"
                      >{{ fetchedAtLabel(provider) }}</span>
                      <span
                        v-if="provider.catalog"
                        class="icon-[mdi--chevron-down] text-[14px] text-[#8f8f8f] transition-transform duration-panel"
                        :class="provider.expanded ? 'rotate-180' : ''"
                      ></span>
                    </span>
                  </div>
                  <div class="mt-1 truncate" :class="probeClass(provider)">{{ probeText(provider) }}</div>
                </button>

                <Transition name="mo-fade-down">
                  <div
                    v-if="importHint.id === provider.id && importHint.text"
                    class="rounded-[8px] border border-[#1d4b2f] bg-[#132a1c] px-3 py-1.5 text-xs text-[#86efac]"
                  >
                    {{ importHint.text }}
                  </div>
                </Transition>

                <!-- 用 v-show 而不是 v-if：picker 内部持有搜索词与勾选集合，
                     卸载重建会把它们清空。高度动画由 JS 钩子用 scrollHeight 显式测量，
                     不用 grid-template-rows 的 0fr→1fr —— 模型列表是异步填充的，
                     fr 过渡在内容尺寸未定时拿不到可靠起始值。 -->
                <Transition :css="false" @enter="collapseEnter" @leave="collapseLeave">
                  <div v-show="provider.expanded" class="mo-collapse">
                    <div class="rounded-[8px] border border-[#343434] bg-[#232323] p-3">
                      <ProviderModelPicker
                        :provider="provider"
                        :models="provider.catalog?.models ?? []"
                        :empty-hint="provider.probing
                          ? '正在获取模型列表...'
                          : provider.catalog?.error || '点击「获取模型」拉取该站点的模型列表'"
                        :reachable="provider.catalog?.reachable || false"
                        :attempts="provider.catalog?.attempts || []"
                        compact
                        @imported="handleImported(provider, $event)"
                        @error="reportError('导入失败', $event)"
                      />
                    </div>
                  </div>
                </Transition>
              </div>

              <!-- 底部一个区块承载「启用开关 + 操作行」：两者原本各自套一层
                   rounded bg-[#232323]，与上方状态区的同款底色叠成三层灰块，
                   把 min-h-[168px] 的内容区挤扁。现在只留一条 border-t 分隔。 -->
              <div class="flex flex-col gap-2.5 border-t border-[#343434] pt-3">
                <Switch
                  compact
                  label="启用"
                  enabled-text="模型已下发给 Cursor"
                  disabled-text="已停用，配置与模型保留"
                  :enabled="!provider.disabled"
                  :busy="provider.toggling"
                  :disabled="appState.configSaving"
                  @change="handleToggleDisabled(provider, $event)"
                />
                <!-- 单行不换行：次要操作收成 icon-only，删除进溢出菜单。
                     一旦允许 flex-wrap，最后一个控件就会折到第二行右下角。 -->
                <div class="center-row justify-end gap-2">
                  <Button
                    variant="default"
                    :disabled="provider.probing"
                    @click="handleFetchAndExpand(provider)"
                  >
                    {{ provider.probing ? "获取中..." : provider.expanded ? "收起" : "获取模型" }}
                  </Button>
                  <!-- 强制刷新入口：请求中由 Button 统一显示 Spinner，成功后短暂显示完成态。 -->
                  <Button
                    v-if="provider.catalog"
                    variant="default"
                    :loading="provider.probing"
                    :aria-label="provider.refreshSucceeded
                      ? `${provider.name} 的模型列表刷新完成`
                      : `刷新 ${provider.name} 的模型列表`"
                    :title="provider.refreshSucceeded
                      ? '刷新完成'
                      : `上次更新 ${fetchedAtLabel(provider) || '-'}`"
                    @click="handleRefresh(provider)"
                  >
                    <Transition name="mo-fade" mode="out-in">
                      <span
                        v-if="!provider.probing"
                        :key="provider.refreshSucceeded ? 'success' : 'refresh'"
                        class="text-[14px]"
                        :class="provider.refreshSucceeded
                          ? 'icon-[mdi--check] text-[#86efac]'
                          : 'icon-[mdi--refresh]'"
                      ></span>
                    </Transition>
                  </Button>
                  <Button
                    variant="default"
                    :disabled="appState.configSaving"
                    :aria-label="`编辑 ${provider.name}`"
                    title="编辑"
                    @click="openEditor(provider)"
                  >
                    <span class="icon-[mdi--pencil] text-[14px]"></span>
                  </Button>
                  <Select
                    :model-value="''"
                    :options="ACTION_OPTIONS"
                    trigger-icon="icon-[mdi--dots-horizontal]"
                    :disabled="appState.configSaving"
                    :aria-label="`${provider.name} 的更多操作`"
                    @update:model-value="handleProviderAction(provider, $event)"
                  />
                </div>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </div>
  </div>
</template>