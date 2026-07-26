<script setup>
import ProviderModelPicker from "@/components/config/ProviderModelPicker.vue";
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import {
  appState,
  countProviderModels,
  deleteProvider,
  fetchProviderModels,
  openProviderEditorWindow,
  toUserError,
} from "@/state/appState";
import { computed, onBeforeUnmount, ref } from "vue";

const emit = defineEmits(["error", "confirm-delete"]);

// 探活状态只存在于本次会话中，不落盘：它反映的是「此刻站点是否可达」。
const probing = ref(new Set());

// 单值控制：同时只展开一个卡片，避免多张列表同时铺开把页面撑爆。
const expandedID = ref("");
const wideID = ref("");
const probingPath = ref({});
const importHint = ref({ id: "", text: "" });
let expandTimer = null;
let hintTimer = null;
const probeTimers = new Map();

const providers = computed(() =>
  appState.providers.map((provider) => ({
    ...provider,
    modelCount: countProviderModels(provider.id),
    catalog: appState.providerModelCatalog[provider.id] ?? null,
    probing: probing.value.has(provider.id),
    probingPath: probingPath.value[provider.id] || "",
    expanded: expandedID.value === provider.id,
    wide: wideID.value === provider.id,
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

async function handleProbe(provider) {
  if (probing.value.has(provider.id)) {
    return;
  }
  const candidates = ["/v1/models", "/models", "/api/v1/models", "模型请求接口"];
  let index = 0;
  probing.value = new Set(probing.value).add(provider.id);
  probingPath.value = { ...probingPath.value, [provider.id]: candidates[index] };
  probeTimers.set(
    provider.id,
    window.setInterval(() => {
      index = Math.min(index + 1, candidates.length - 1);
      probingPath.value = { ...probingPath.value, [provider.id]: candidates[index] };
    }, 1100),
  );
  try {
    await fetchProviderModels(provider);
  } catch (error) {
    reportError("获取模型失败", toUserError(error));
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

function closeExpanded() {
  expandedID.value = "";
  window.clearTimeout(expandTimer);
  expandTimer = window.setTimeout(() => {
    wideID.value = "";
  }, 220);
}

function openExpanded(providerID) {
  window.clearTimeout(expandTimer);
  if (expandedID.value && expandedID.value !== providerID) {
    expandedID.value = "";
    expandTimer = window.setTimeout(() => {
      wideID.value = providerID;
      requestAnimationFrame(() => {
        expandedID.value = providerID;
      });
    }, 180);
    return;
  }
  wideID.value = providerID;
  requestAnimationFrame(() => {
    expandedID.value = providerID;
  });
}

// 「获取模型」把探活与展开合并成一个动作：拉取本身就是最真实的可用性验证。
async function handleFetchAndExpand(provider) {
  if (provider.expanded) {
    closeExpanded();
    return;
  }
  importHint.value = { id: "", text: "" };
  openExpanded(provider.id);
  await handleProbe(provider);
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

onBeforeUnmount(() => {
  window.clearTimeout(expandTimer);
  window.clearTimeout(hintTimer);
  for (const timer of probeTimers.values()) {
    window.clearInterval(timer);
  }
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
        <div class="grid gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(280px,1fr))]">
          <Card
            v-for="provider in providers"
            :key="provider.id"
            :class="provider.wide ? '[grid-column:1/-1]' : ''"
          >
            <div class="flex h-full min-h-[168px] flex-col justify-between gap-3">
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
                    <span
                      v-if="provider.catalog"
                      class="shrink-0 text-[14px] text-[#8f8f8f]"
                      :class="provider.expanded ? 'icon-[mdi--chevron-up]' : 'icon-[mdi--chevron-down]'"
                    ></span>
                  </div>
                  <div class="mt-1 truncate" :class="probeClass(provider)">{{ probeText(provider) }}</div>
                </button>

                <Transition name="hint-fade">
                  <div
                    v-if="importHint.id === provider.id && importHint.text"
                    class="rounded-[8px] border border-[#1d4b2f] bg-[#132a1c] px-3 py-1.5 text-xs text-[#86efac]"
                  >
                    {{ importHint.text }}
                  </div>
                </Transition>

                <div
                  class="provider-expand-grid"
                  :class="provider.expanded ? 'provider-expand-grid--open' : ''"
                >
                  <div class="min-h-0 overflow-hidden">
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
                </div>
              </div>

              <div class="center-row flex-wrap justify-end gap-2 border-t border-[#343434] pt-3">
                <Button
                  variant="default"
                  :disabled="provider.probing"
                  @click="handleFetchAndExpand(provider)"
                >
                  {{ provider.probing ? "获取中..." : provider.expanded ? "收起" : "获取模型" }}
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

<style scoped>
.provider-expand-grid {
  display: grid;
  grid-template-rows: 0fr;
  margin-top: -10px;
  opacity: 0;
  visibility: hidden;
  transition:
    grid-template-rows 220ms ease,
    margin-top 220ms ease,
    opacity 160ms ease,
    visibility 0s linear 220ms;
}

.provider-expand-grid--open {
  grid-template-rows: 1fr;
  margin-top: 0;
  opacity: 1;
  visibility: visible;
  transition:
    grid-template-rows 220ms ease,
    opacity 180ms ease,
    visibility 0s;
}

.hint-fade-enter-active,
.hint-fade-leave-active {
  transition: opacity 160ms ease, transform 180ms ease;
}

.hint-fade-enter-from,
.hint-fade-leave-to {
  opacity: 0;
  transform: translateY(-3px);
}
</style>