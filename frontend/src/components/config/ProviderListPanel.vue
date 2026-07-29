<script setup>
import ProviderModelPicker from "@/components/config/ProviderModelPicker.vue";
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import {
  appState,
  countProviderModels,
  deleteProvider,
  fetchProviderModels,
  hasCachedProviderModels,
  openProviderEditorWindow,
  toUserError,
} from "@/state/appState";
import dayjs from "dayjs";
import { computed, onBeforeUnmount, onDeactivated, ref } from "vue";

const emit = defineEmits(["error", "confirm-delete"]);

// 探活状态只存在于本次会话中，不落盘：它反映的是「此刻站点是否可达」。
const probing = ref(new Set());

// 单值控制：同时只展开一个卡片，避免多张列表同时铺开把页面撑爆。
// 卡片原地展开（不再跨列），所以这一个 ref 就是完整的展开状态 ——
// 展开与收起互斥、只展开一张，都由「赋值」本身表达。
const expandedID = ref("");
const probingPath = ref({});
const refreshSucceeded = ref(new Set());
const importHint = ref({ id: "", text: "" });
let hintTimer = null;
const probeTimers = new Map();
const refreshFeedbackTimers = new Map();

const providers = computed(() =>
  appState.providers.map((provider) => ({
    ...provider,
    modelCount: countProviderModels(provider.id),
    catalog: appState.providerModelCatalog[provider.id] ?? null,
    probing: probing.value.has(provider.id),
    probingPath: probingPath.value[provider.id] || "",
    refreshSucceeded: refreshSucceeded.value.has(provider.id),
    expanded: expandedID.value === provider.id,
  })),
);

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

function handleImported(provider, { added }) {
  window.clearTimeout(hintTimer);
  importHint.value = {
    id: provider.id,
    text: added > 0 ? `已导入 ${added} 个模型` : "所选模型均已存在，未新增",
  };
  hintTimer = window.setTimeout(() => {
    importHint.value = { id: "", text: "" };
  }, 3000);
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
// 少了这一句，路径轮播的 setInterval 会在隐藏的面板上一直滴答。
onDeactivated(clearAllTimers);

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
    <div class="flex shrink-0 items-center justify-between gap-4 pb-4">
      <div class="text-sm text-[#a3a3a3]">
        中转站提供接口地址与密钥，其下的模型自动继承，无需逐个填写。
      </div>
      <Button variant="primary" :disabled="appState.configSaving" @click="openEditor()">新增中转站</Button>
    </div>

    <div class="min-h-0 flex-1">
      <div
        v-if="providers.length === 0"
        class="flex h-full min-h-[220px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]"
      >
        还没有可用的中转站。
      </div>

      <div v-else class="h-full min-h-0 overflow-y-auto pr-1">
        <!-- items-start 是必须的：grid 默认拉伸同行所有单元格，
             展开的卡片会把邻居一起顶到同样高度，留下大片空白。 -->
        <div
          class="grid items-start gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(280px,1fr))]"
        >
          <Card v-for="provider in providers" :key="provider.id">
            <div class="flex min-h-[168px] flex-col justify-between gap-3">
              <div class="flex flex-col gap-2.5">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0 flex-1">
                    <div class="center-row justify-start gap-2">
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

              <div class="center-row flex-wrap justify-end gap-2 border-t border-[#343434] pt-3">
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
                <Button variant="default" :disabled="appState.configSaving" @click="openEditor(provider)">编辑</Button>
                <Button variant="text" :disabled="appState.configSaving" @click="handleDelete(provider)">删除</Button>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </div>
  </div>
</template>