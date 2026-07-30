<script setup>
import ProviderModelPicker from "@/components/config/ProviderModelPicker.vue";
import Button from "@/components/ui/Button.vue";
import Input from "@/components/ui/Input.vue";
import Select from "@/components/ui/Select.vue";
import Skeleton from "@/components/ui/Skeleton.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { getProviderEditorContext } from "@/services/clientApi";
import {
  appState,
  CLIENT_PROFILE_OPTIONS,
  createEmptyProvider,
  fetchProviderModels,
  formatImportSummary,
  normalizeProvider,
  saveProvider,
  toUserError,
  validateProviderDetails,
} from "@/state/appState";
import { Window } from "@wailsio/runtime";
import { computed, nextTick, onMounted, reactive, ref } from "vue";

const providerTypeTabs = [
  { label: "OpenAI", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "Anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

// 高级覆盖值；通常应优先选择“客户端模式”，由运行时生成完整且一致的请求头。
const userAgentPresets = [
  { label: "不指定（使用默认）", value: "", icon: "icon-[mdi--web]" },
  { label: "claude-cli/2.1.158 (external, sdk-cli)", value: "claude-cli/2.1.158 (external, sdk-cli)", icon: "icon-[logos--claude-icon]" },
];

const fieldTips = {
  name: "中转站的展示名称，也会作为该站下模型的默认备注。",
  type: "决定该站点使用 OpenAI 还是 Anthropic 协议进行认证与请求。",
  baseURL: "中转站的 API 根地址，例如 https://anyrouter.top。",
  apiKey: "该中转站的访问密钥。填写后，站点下的所有模型都会继承它。",
  clientProfile: "通用协议只发送必要请求头；Claude Code 和 Codex 模式会发送对应客户端的兼容请求指纹。自定义请求头仍可覆盖这些默认值。",
  userAgent: "部分中转站按 User-Agent 做白名单，UA 不匹配会直接拒绝而不进入认证。",
  headersJSON: "附加请求头 JSON 对象，值必须是字符串。会与模型自身的自定义请求头合并，模型优先。",
  modelsPath: "拉取模型列表的路径。留空时自动探测 /v1/models、/models、/api/v1/models。",
  inferencePath: "最近一次连接探测命中的推理端点，仅用于诊断；真实请求路径由模型协议在请求时派生。",
  note: "仅本地展示的说明文字。",
};

const draft = reactive(createEmptyProvider());
const loading = ref(true);
const errorMessage = ref("");
const fetching = ref(false);
const statusMessage = ref("");
const advancedOpen = ref(false);
const invalidField = ref("");
const nameInputRef = ref(null);
const baseURLInputRef = ref(null);
const typeTabsRef = ref(null);
const clientProfileRef = ref(null);
const headersJSONRef = ref(null);
// 窗口打开时是否为「新增」。套用预设后 draft.id 会有值，用它判断会让预设区块
// 当场消失，用户看不到自己刚点的结果，因此单独记一次初始态。
const startedAsNew = ref(false);
const selectedPresetID = ref("");

const builtinPresets = computed(() =>
  appState.providers.filter((provider) => provider.builtin),
);
const builtinPresetOptions = computed(() => [
  { label: "不使用预设（自定义）", value: "", icon: "icon-[mdi--tune-variant]" },
  ...builtinPresets.value.map((provider) => ({
    label: `${provider.name} · ${provider.type === "anthropic" ? "Anthropic" : "OpenAI"}`,
    value: provider.id,
    icon: provider.type === "anthropic" ? "icon-[logos--claude-icon]" : "icon-[bxl--openai]",
  })),
]);

const isEditing = computed(() => Boolean(draft.id));
const title = computed(() => (startedAsNew.value || !isEditing.value ? "新增中转站" : "编辑中转站"));
const catalogKey = computed(() => draft.id || draft.baseURL);
const catalog = computed(() => appState.providerModelCatalog[catalogKey.value] ?? null);
const models = computed(() => catalog.value?.models ?? []);
const modelCountText = computed(() => `共 ${models.value.length} 个模型`);
const hasCustomRequestPaths = computed(() => Boolean(draft.modelsPath));
const detectedInferencePath = computed(() => catalog.value?.inferencePath || draft.inferencePath || "尚未探测");
const advancedSummary = computed(() => {
  const configured = [];
  if (draft.userAgent) {
    configured.push("User-Agent");
  }
  if (draft.headersJSON) {
    configured.push("自定义请求头");
  }
  if (draft.note) {
    configured.push("备注");
  }
  return configured.length > 0 ? `已配置：${configured.join("、")}` : "未配置额外覆盖";
});

const baseURLPlaceholder = computed(() =>
  draft.type === "anthropic" ? "例如：https://api.anthropic.com" : "例如：https://anyrouter.top",
);

const headersPlaceholder = JSON.stringify({ "X-Api-Version": "2024-01-01" });

function applyBuiltinPreset(providerID) {
  const id = String(providerID || "").trim();
  selectedPresetID.value = id;
  invalidField.value = "";
  const currentAPIKey = draft.apiKey;
  if (!id) {
    Object.assign(draft, createEmptyProvider(), { apiKey: currentAPIKey });
    errorMessage.value = "";
    statusMessage.value = "已切换为自定义中转站，请填写名称和接口地址";
    return;
  }

  const preset = builtinPresets.value.find((provider) => provider.id === id);
  if (!preset) {
    errorMessage.value = "所选内置预设已不存在";
    statusMessage.value = "";
    return;
  }

  Object.assign(draft, normalizeProvider({
    ...preset,
    apiKey: currentAPIKey || preset.apiKey,
  }));
  errorMessage.value = "";
  statusMessage.value = `已套用「${preset.name}」预设，只需填写访问密钥`;
}

function handleImported(summary) {
  errorMessage.value = "";
  statusMessage.value = formatImportSummary(summary);
}

function handlePickerError(message) {
  statusMessage.value = "";
  errorMessage.value = message;
}

function resetRequestPaths() {
  draft.modelsPath = "";
  errorMessage.value = "";
  statusMessage.value = "已恢复自动探测，保存后生效";
}

async function loadContext() {
  try {
    const ctx = await getProviderEditorContext();
    const parsed = JSON.parse(ctx?.providerJSON || "{}");
    const provider = normalizeProvider(parsed);
    startedAsNew.value = !provider.id;
    selectedPresetID.value = provider.builtin ? provider.id : "";
    Object.assign(draft, provider);
    if (!draft.type) {
      draft.type = "openai";
    }
  } catch (_error) {
    startedAsNew.value = true;
    selectedPresetID.value = "";
    Object.assign(draft, createEmptyProvider());
  } finally {
    loading.value = false;
  }
}

async function focusValidationField(field) {
  if (field === "headersJSON") {
    advancedOpen.value = true;
  }
  await nextTick();
  const target = {
    name: nameInputRef.value,
    baseURL: baseURLInputRef.value,
    type: typeTabsRef.value?.querySelector("button"),
    clientProfile: clientProfileRef.value?.$el?.querySelector("button"),
    headersJSON: headersJSONRef.value,
  }[field];
  target?.focus?.();
}

function clearValidationField(field) {
  if (invalidField.value !== field) {
    return;
  }
  invalidField.value = "";
  errorMessage.value = "";
}

// persistDraft 是「拉取模型」与「导入模型」的共同前置：
// 这两个动作都需要一个已落盘的 provider id 才能建立归属关系。
async function persistDraft() {
  const provider = normalizeProvider(draft);
  const validation = validateProviderDetails([provider]);
  if (validation) {
    invalidField.value = validation.field;
    errorMessage.value = validation.message;
    statusMessage.value = "";
    await focusValidationField(validation.field);
    return { ok: false, error: validation.message, provider: null };
  }

  invalidField.value = "";
  const result = await saveProvider(provider);
  if (!result.ok) {
    errorMessage.value = result.error;
    return { ok: false, error: result.error, provider: null };
  }
  if (result.provider) {
    Object.assign(draft, result.provider);
  }
  errorMessage.value = "";
  return { ok: true, error: "", provider: result.provider ?? provider };
}

async function handleSave() {
  const result = await persistDraft();
  if (!result.ok) {
    return;
  }
  await Window.Close();
}

async function handleCancel() {
  await Window.Close();
}

async function handleFetchModels() {
  statusMessage.value = "";
  fetching.value = true;
  try {
    const saved = await persistDraft();
    if (!saved.ok) {
      return;
    }
    const result = await fetchProviderModels(saved.provider);
    if (result.provider) {
      Object.assign(draft, result.provider);
    }
    if (!result.ok && !result.result?.reachable) {
      errorMessage.value = result.error || "拉取模型列表失败";
      return;
    }
    errorMessage.value = "";
    statusMessage.value = result.ok
      ? `已获取 ${result.result.models.length} 个模型`
      : "接口可达，但未提供模型列表；请手动添加模型 ID";
  } catch (error) {
    errorMessage.value = toUserError(error);
  } finally {
    fetching.value = false;
  }
}

onMounted(async () => {
  await loadContext();
});
</script>

<template>
  <div class="flex h-full flex-col text-[#e5e5e5]">
    <div class="flex shrink-0 flex-wrap items-center justify-between gap-2 px-4 pb-2">
      <h2 class="text-base font-medium text-white">{{ title }}</h2>
      <div class="flex flex-wrap items-center justify-end gap-2">
        <Button variant="default" @click="handleCancel">取消</Button>
        <Button
          variant="default"
          :disabled="fetching || appState.configSaving"
          :loading="fetching"
          @click="handleFetchModels"
        >
          {{ fetching ? "拉取中..." : "保存并拉取模型" }}
        </Button>
        <Button variant="primary" :disabled="appState.configSaving" :loading="appState.configSaving" @click="handleSave">
          {{ appState.configSaving ? "保存中..." : "保存" }}
        </Button>
      </div>
    </div>

    <div
      v-if="statusMessage || errorMessage"
      class="shrink-0 px-4 pb-3"
      role="status"
      aria-live="polite"
    >
      <div
        v-if="errorMessage"
        class="rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]"
      >
        {{ errorMessage }}
      </div>
      <div
        v-else
        class="rounded-[8px] border border-[#1d4b2f] bg-[#132a1c] px-3 py-2 text-sm text-[#86efac]"
      >
        {{ statusMessage }}
      </div>
    </div>

    <div v-if="loading" class="min-h-0 flex-1 overflow-hidden px-4 pb-4" aria-busy="true">
      <Skeleton variant="text" :lines="6" />
    </div>

    <div v-else class="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
      <div class="flex flex-col gap-3">
        <section
          v-if="startedAsNew && builtinPresetOptions.length > 1"
          class="rounded-[10px] border border-[#28563b] bg-[#17271e] p-3.5"
        >
          <div class="grid grid-cols-1 items-end gap-3 sm:grid-cols-[minmax(0,320px)_1fr]">
            <label class="flex flex-col gap-1">
              <span class="text-sm font-medium text-[#d8f3e2]">快速套用内置预设</span>
              <Select
                :model-value="selectedPresetID"
                :options="builtinPresetOptions"
                placeholder="选择中转站预设"
                aria-label="中转站内置预设"
                @update:model-value="applyBuiltinPreset"
              />
            </label>
            <p class="text-xs leading-5 text-[#9bb8a5]">
              预设会填充名称、协议、根地址和客户端模式，不包含密钥；保存时复用内置站点，不会创建重复项。
            </p>
          </div>
        </section>

        <section class="rounded-[10px] border border-[#343434] bg-[#252525] p-3.5">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div>
              <h3 class="text-sm font-medium text-white">基础连接</h3>
              <p class="mt-1 text-xs text-[#8f8f8f]">设置站点地址、密钥和默认客户端模式。</p>
            </div>
            <div class="center-row gap-2">
              <span
                v-if="draft.builtin"
                class="center-row gap-1 rounded-[999px] border border-[#3f3f3f] px-[7px] py-[4px] text-[11px] text-[#cfcfcf]"
              >
                内置预设
              </span>
              <div
                ref="typeTabsRef"
                class="center-row gap-1.5 rounded-[8px]"
                :class="invalidField === 'type' ? 'ring-2 ring-[#ef4444]' : ''"
              >
                <button
                  v-for="tab in providerTypeTabs"
                  :key="tab.value"
                  type="button"
                  class="center-row gap-2 rounded-[8px] border px-3 py-1.5 text-sm transition-colors duration-150"
                  :class="draft.type === tab.value
                    ? 'border-[#1ca35a] bg-[#123322] text-white'
                    : 'border-[#343434] bg-[#202020] text-[#a3a3a3] hover:border-[#4a4a4a] hover:text-[#e5e5e5]'"
                  @click="draft.type = tab.value; clearValidationField('type')"
                >
                  <span :class="[tab.icon, 'text-[16px]']"></span>
                  <span>{{ tab.label }}</span>
                </button>
              </div>
            </div>
          </div>

          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="flex flex-col gap-1">
              <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                <Tooltip :content="fieldTips.name" />
                <span>中转站名称</span>
              </span>
              <input
                ref="nameInputRef"
                v-model="draft.name"
                type="text"
                placeholder="例如：AnyRouter"
                :class="[
                  'h-9 rounded-[6px] border bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]',
                  invalidField === 'name' ? 'border-[#ef4444] ring-1 ring-[#ef4444]' : 'border-[#3f3f3f]',
                ]"
                @input="clearValidationField('name')"
              />
            </label>

            <label class="flex flex-col gap-1">
              <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                <Tooltip :content="fieldTips.baseURL" />
                <span>接口地址</span>
              </span>
              <input
                ref="baseURLInputRef"
                v-model="draft.baseURL"
                type="text"
                :placeholder="baseURLPlaceholder"
                :class="[
                  'h-9 rounded-[6px] border bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]',
                  invalidField === 'baseURL' ? 'border-[#ef4444] ring-1 ring-[#ef4444]' : 'border-[#3f3f3f]',
                ]"
                @input="clearValidationField('baseURL')"
              />
            </label>

            <label class="flex flex-col gap-1">
              <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                <Tooltip :content="fieldTips.apiKey" />
                <span>访问密钥</span>
              </span>
              <Input
                v-model="draft.apiKey"
                type="password"
                allow-visibility-toggle
                placeholder="例如：sk-xxxxxx"
                autocomplete="off"
              />
            </label>

            <label class="flex flex-col gap-1">
              <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                <Tooltip :content="fieldTips.clientProfile" />
                <span>客户端模式</span>
              </span>
              <Select
                ref="clientProfileRef"
                v-model="draft.clientProfile"
                :options="CLIENT_PROFILE_OPTIONS"
                :button-class="invalidField === 'clientProfile' ? '!border-[#ef4444] ring-1 ring-[#ef4444]' : ''"
                @update:model-value="clearValidationField('clientProfile')"
              />
            </label>
          </div>
        </section>

        <section class="rounded-[10px] border border-[#343434] bg-[#252525] p-3.5">
          <div class="mb-3 flex items-center justify-between gap-3">
            <div>
              <h3 class="text-sm font-medium text-white">模型列表与探测诊断</h3>
              <p class="mt-1 text-xs text-[#8f8f8f]">模型列表路径可留空自动探测；推理端点仅作诊断，真实请求由模型协议派生。</p>
            </div>
            <Button
              variant="text"
              :disabled="!hasCustomRequestPaths"
              @click="resetRequestPaths"
            >
              恢复自动探测
            </Button>
          </div>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="flex flex-col gap-1">
              <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                <Tooltip :content="fieldTips.modelsPath" />
                <span>模型列表路径</span>
              </span>
              <input
                v-model="draft.modelsPath"
                type="text"
                placeholder="留空自动探测，例如：/v1/models"
                class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]"
              />
            </label>

            <label class="flex flex-col gap-1">
              <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                <Tooltip :content="fieldTips.inferencePath" />
                <span>探测到的推理端点</span>
              </span>
              <input
                :value="detectedInferencePath"
                type="text"
                readonly
                class="h-9 rounded-[6px] border border-[#343434] bg-[#1c1c1c] px-3 text-sm text-[#8f8f8f] outline-none"
              />
            </label>
          </div>
        </section>

        <section class="rounded-[10px] border border-[#343434] bg-[#252525] p-3.5">
          <div class="flex items-center justify-between gap-3 pb-2.5">
            <div>
              <h3 class="text-sm font-medium text-white">可用模型</h3>
              <p class="mt-1 text-xs text-[#8f8f8f]">搜索、筛选并批量导入该站点提供的模型。</p>
            </div>
            <span v-if="models.length > 0" class="text-xs text-[#8f8f8f]">
              {{ modelCountText }}
            </span>
          </div>

          <ProviderModelPicker
            v-if="draft.id"
            :provider="draft"
            :models="models"
            :request-url="catalog?.requestURL || ''"
            :empty-hint="catalog?.error || '点击「保存并拉取模型」获取该站点的模型列表'"
            :reachable="catalog?.reachable || false"
            :attempts="catalog?.attempts || []"
            @imported="handleImported"
            @error="handlePickerError"
          />
          <div
            v-else
            class="rounded-[6px] border border-dashed border-[#3a3a3a] bg-[#232323] px-3 py-6 text-center text-sm text-[#a3a3a3]"
          >
            点击「保存并拉取模型」获取该站点的模型列表
          </div>
        </section>

        <section class="overflow-hidden rounded-[10px] border border-[#343434] bg-[#252525]">
          <button
            type="button"
            class="flex w-full items-center justify-between gap-3 px-3.5 py-3 text-left transition-colors hover:bg-[#2a2a2a]"
            :aria-expanded="advancedOpen"
            @click="advancedOpen = !advancedOpen"
          >
            <span>
              <span class="block text-sm font-medium text-white">高级请求设置</span>
              <span class="mt-1 block text-xs text-[#8f8f8f]">{{ advancedSummary }}</span>
            </span>
            <span
              class="icon-[mdi--chevron-down] text-[20px] text-[#a3a3a3] transition-transform duration-200"
              :class="advancedOpen ? 'rotate-180' : ''"
            ></span>
          </button>

          <div v-show="advancedOpen" class="border-t border-[#343434] px-3.5 py-3">
            <div class="flex flex-col gap-3">
              <div class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                  <Tooltip :content="fieldTips.userAgent" />
                  <span>User-Agent</span>
                </span>
                <div class="grid grid-cols-1 gap-2 sm:grid-cols-[minmax(0,260px)_1fr]">
                  <Select
                    :model-value="userAgentPresets.some((item) => item.value === draft.userAgent) ? draft.userAgent : ''"
                    :options="userAgentPresets"
                    aria-label="User-Agent 预设"
                    @update:model-value="(value) => { draft.userAgent = value; }"
                  />
                  <input
                    v-model="draft.userAgent"
                    type="text"
                    placeholder="也可直接填写自定义 UA"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]"
                  />
                </div>
              </div>

              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                  <Tooltip :content="fieldTips.headersJSON" />
                  <span>自定义请求头 JSON</span>
                </span>
                <textarea
                  ref="headersJSONRef"
                  v-model="draft.headersJSON"
                  rows="4"
                  spellcheck="false"
                  :placeholder="headersPlaceholder"
                  :class="[
                    'min-h-[96px] w-full resize-y rounded-[6px] border bg-[#1f1f1f] px-3 py-2 font-mono text-xs text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]',
                    invalidField === 'headersJSON' ? 'border-[#ef4444] ring-1 ring-[#ef4444]' : 'border-[#3f3f3f]',
                  ]"
                  @input="clearValidationField('headersJSON')"
                />
              </label>

              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                  <Tooltip :content="fieldTips.note" />
                  <span>备注</span>
                </span>
                <textarea
                  v-model="draft.note"
                  rows="2"
                  placeholder="例如：主力站点，额度按天重置"
                  class="min-h-[64px] resize-y rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 py-2 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]"
                />
              </label>
            </div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>
