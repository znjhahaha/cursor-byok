<script setup>
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import { appState, importProviderModels, toUserError } from "@/state/appState";
import {
  buildAdapterDisplayName,
  MODEL_TYPE_ANTHROPIC,
  OPENAI_ENDPOINT_RESPONSES,
  PROTOCOL_OVERRIDE_ANTHROPIC,
  PROTOCOL_OVERRIDE_AUTO,
  PROTOCOL_OVERRIDE_OPENAI_CHAT,
  PROTOCOL_OVERRIDE_OPENAI_RESPONSES,
  resolveModelProtocol,
} from "@/state/modelFamily";
import { computed, nextTick, ref, watch } from "vue";

// 家族筛选是纯字面量匹配，不查表也不请求：模型 ID 里几乎总能看到厂商前缀，
// 用它做粗筛比维护一份会过期的模型清单更稳。
const MODEL_FAMILIES = [
  { key: "claude", label: "Claude" },
  { key: "gpt", label: "GPT" },
  { key: "gemini", label: "Gemini" },
  { key: "grok", label: "Grok" },
  { key: "deepseek", label: "DeepSeek" },
  { key: "qwen", label: "Qwen" },
];

// 协议覆盖只影响本次导入，不落到中转站上：站点仍然只负责连接信息。
const PROTOCOL_OVERRIDE_OPTIONS = [
  { label: "按模型名自动匹配协议", value: PROTOCOL_OVERRIDE_AUTO, icon: "icon-[mdi--auto-fix]" },
  { label: "全部按 Anthropic 导入", value: PROTOCOL_OVERRIDE_ANTHROPIC, icon: "icon-[logos--claude-icon]" },
  { label: "全部按 OpenAI Responses 导入", value: PROTOCOL_OVERRIDE_OPENAI_RESPONSES, icon: "icon-[bxl--openai]" },
  { label: "全部按 OpenAI Chat 导入", value: PROTOCOL_OVERRIDE_OPENAI_CHAT, icon: "icon-[mdi--message-text-outline]" },
];

const props = defineProps({
  // provider 必须是已落盘的对象：导入依赖 provider.id 建立归属关系。
  provider: {
    type: Object,
    required: true,
  },
  models: {
    type: Array,
    default: () => [],
  },
  requestURL: {
    type: String,
    default: "",
  },
  emptyHint: {
    type: String,
    default: "尚未拉取模型列表",
  },
  reachable: {
    type: Boolean,
    default: false,
  },
  attempts: {
    type: Array,
    default: () => [],
  },
  // compact 用于卡片内联展开：收紧列宽与列表高度，避免撑破卡片。
  compact: {
    type: Boolean,
    default: false,
  },
});

const emit = defineEmits(["imported", "error"]);

const keyword = ref("");
const family = ref("");
const hideImported = ref(true);
const selected = ref(new Set());
const importing = ref(false);
const manualModelID = ref("");
const listRef = ref(null);
const protocolOverride = ref(PROTOCOL_OVERRIDE_AUTO);

// 徽章只显示协议形态，不显示客户端指纹：指纹是由协议+端点推导出来的，
// 单独列出来会让用户以为它是可以独立选择的第三个维度。
function protocolBadge(protocol) {
  if (protocol.type === MODEL_TYPE_ANTHROPIC) {
    return { text: "Anthropic", icon: "icon-[logos--claude-icon]" };
  }
  return {
    text: protocol.openAIEndpoint === OPENAI_ENDPOINT_RESPONSES ? "OpenAI · Responses" : "OpenAI · Chat",
    icon: "icon-[bxl--openai]",
  };
}

// 已导入判定用 providerID + modelID 精确相等；用户手改过 modelID 的条目会被判为未导入，
// 由 importProviderModels 内部的去重兜底，不会产生重复条目。
const importedIDs = computed(() => {
  const providerID = String(props.provider?.id || "");
  if (!providerID) {
    return new Set();
  }
  return new Set(
    (appState.modelAdapters ?? [])
      .filter((item) => item?.providerID === providerID)
      .map((item) => String(item?.modelID || "")),
  );
});

const decorated = computed(() =>
  props.models.map((model) => {
    const id = String(model?.id || "");
    // 预览与真正落盘走同一组纯函数，避免「预览显示 Anthropic、导进去却是 OpenAI」。
    const protocol = resolveModelProtocol(id, props.provider, protocolOverride.value);
    return {
      id,
      lower: id.toLowerCase(),
      imported: importedIDs.value.has(id),
      protocol,
      badge: protocolBadge(protocol),
      previewName: buildAdapterDisplayName({
        providerName: props.provider?.name || "",
        modelID: id,
        template: props.provider?.nameTemplate || "",
      }),
    };
  }),
);

// 只渲染实际有匹配的家族按钮，避免出现点了没结果的死按钮。
const availableFamilies = computed(() =>
  MODEL_FAMILIES.filter((item) => decorated.value.some((model) => model.lower.includes(item.key))),
);

const visibleModels = computed(() => {
  const text = keyword.value.trim().toLowerCase();
  return decorated.value.filter((model) => {
    if (hideImported.value && model.imported) {
      return false;
    }
    if (family.value && !model.lower.includes(family.value)) {
      return false;
    }
    return !text || model.lower.includes(text);
  });
});

// 全选语义限定在「当前筛选结果中未导入的项」，而不是整个列表。
const selectableIDs = computed(() =>
  visibleModels.value.filter((model) => !model.imported).map((model) => model.id),
);

const selectedCount = computed(() => selected.value.size);

const allSelected = computed(
  () =>
    selectableIDs.value.length > 0 &&
    selectableIDs.value.every((id) => selected.value.has(id)),
);

const importedCount = computed(() => decorated.value.filter((model) => model.imported).length);

// 切换中转站时重置全部交互状态，避免上一个站的选中集合泄漏到下一个站。
watch(
  () => props.provider?.id,
  () => {
    selected.value = new Set();
    keyword.value = "";
    family.value = "";
    protocolOverride.value = PROTOCOL_OVERRIDE_AUTO;
  },
);

function toggleModel(model) {
  if (model.imported) {
    return;
  }
  const next = new Set(selected.value);
  if (next.has(model.id)) {
    next.delete(model.id);
  } else {
    next.add(model.id);
  }
  selected.value = next;
}

function toggleSelectAll() {
  if (allSelected.value) {
    selected.value = new Set();
    return;
  }
  selected.value = new Set(selectableIDs.value);
}

function toggleFamily(key) {
  family.value = family.value === key ? "" : key;
}

async function importModelIDs(modelIDs) {
  if (modelIDs.length === 0 || importing.value) {
    return;
  }
  const scrollTop = listRef.value?.scrollTop ?? 0;
  importing.value = true;
  try {
    const result = await importProviderModels(props.provider.id, modelIDs, {
      protocolOverride: protocolOverride.value,
    });
    if (!result.ok) {
      emit("error", result.error || "导入失败");
      return;
    }
    selected.value = new Set();
    manualModelID.value = "";
    emit("imported", {
      added: result.added ?? 0,
      skipped: result.skipped ?? 0,
      requested: result.requested ?? modelIDs.length,
    });
    await nextTick();
    if (listRef.value) {
      listRef.value.scrollTop = scrollTop;
    }
  } catch (error) {
    emit("error", toUserError(error));
  } finally {
    importing.value = false;
  }
}

function handleImport() {
  return importModelIDs(Array.from(selected.value));
}

function handleManualImport() {
  const modelID = manualModelID.value.trim();
  if (!modelID) {
    return;
  }
  return importModelIDs([modelID]);
}
</script>

<template>
  <div class="flex min-h-0 flex-col gap-2.5">
    <!-- compact 用在中转站卡片内联展开处，容器只有约 300px：
         四件控件挤一行会折成三行，所以纵向分成「筛选」「批量操作」两层。
         宽容器（编辑器窗口）仍保持单行。 -->
    <div
      v-if="models.length > 0"
      class="flex gap-2"
      :class="compact ? 'flex-col' : 'center-row flex-wrap justify-between'"
    >
      <div class="center-row min-w-0 flex-1 gap-2">
        <div class="relative min-w-0 flex-1">
          <span
            class="icon-[mdi--magnify] pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-[15px] text-[#737373]"
          ></span>
          <input
            v-model="keyword"
            type="text"
            spellcheck="false"
            placeholder="搜索模型 ID"
            class="h-8 w-full rounded-[6px] border border-[#3f3f3f] bg-[#232323] pl-7 pr-2 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]"
          />
        </div>
        <label class="center-row shrink-0 cursor-pointer gap-1.5 text-xs text-[#a3a3a3]">
          <input v-model="hideImported" type="checkbox" class="size-3.5 accent-[#10AD5D]" />
          <span>隐藏已添加</span>
        </label>
      </div>
      <div class="center-row shrink-0 gap-2" :class="compact ? 'justify-end' : ''">
        <Button v-if="selectableIDs.length > 0" variant="text" @click="toggleSelectAll">
          {{ allSelected ? "取消全选" : "全选" }}
        </Button>
        <Button
          variant="default"
          :disabled="importing || selectedCount === 0 || appState.configSaving"
          :loading="importing"
          @click="handleImport"
        >
          {{ importing ? "导入中..." : `导入所选 (${selectedCount})` }}
        </Button>
      </div>
    </div>

    <div v-if="availableFamilies.length > 0" class="center-row flex-wrap justify-start gap-1.5">
      <button
        v-for="item in availableFamilies"
        :key="item.key"
        type="button"
        class="rounded-[999px] border px-2 py-0.5 text-[11px] transition-colors duration-150"
        :class="family === item.key
          ? 'border-[#1ca35a] bg-[#123322] text-white'
          : 'border-[#3a3a3a] bg-[#232323] text-[#a3a3a3] hover:border-[#4a4a4a] hover:text-[#e5e5e5]'"
        @click="toggleFamily(item.key)"
      >
        {{ item.label }}
      </button>
    </div>

    <div v-if="models.length > 0" class="flex flex-col gap-1">
      <Select
        v-model="protocolOverride"
        :options="PROTOCOL_OVERRIDE_OPTIONS"
        aria-label="导入协议"
      />
      <span class="text-[11px] leading-4 text-[#737373]">
        Claude 走 Anthropic 协议，GPT-5 / Codex 走 OpenAI Responses，其余走 OpenAI Chat；客户端模拟随协议一并匹配。
      </span>
    </div>

    <div v-if="requestURL" class="truncate text-xs text-[#737373]" :title="requestURL">
      {{ requestURL }}
    </div>

    <div
      v-if="models.length === 0 && reachable"
      class="rounded-[8px] border border-[#3a3a3a] bg-[#232323] p-3"
    >
      <div class="center-row justify-start gap-2 text-sm text-[#d4d4d4]">
        <span class="icon-[mdi--check-circle-outline] text-[16px] text-[#86efac]"></span>
        <span>接口可达，但站点没有返回模型列表</span>
      </div>
      <div class="mt-2 center-row gap-2">
        <input
          v-model="manualModelID"
          type="text"
          spellcheck="false"
          placeholder="输入模型 ID，例如 claude-sonnet-4-5"
          class="h-8 min-w-0 flex-1 rounded-[6px] border border-[#3f3f3f] bg-[#1f1f1f] px-2.5 text-sm text-[#e5e5e5] outline-none transition-colors focus:border-[#10AD5D]"
          @keydown.enter.prevent="handleManualImport"
        />
        <Button
          variant="primary"
          :disabled="importing || !manualModelID.trim() || appState.configSaving"
          :loading="importing"
          @click="handleManualImport"
        >
          {{ importing ? "添加中..." : "添加模型" }}
        </Button>
      </div>
      <div
        v-if="attempts.length > 0"
        class="mt-2 truncate text-[11px] text-[#666]"
        :title="attempts.join('\n')"
      >
        已尝试 {{ attempts.length }} 个 API 路径，悬停查看详情
      </div>
    </div>

    <div
      v-else-if="models.length === 0"
      class="rounded-[6px] border border-dashed border-[#3a3a3a] bg-[#232323] px-3 py-5 text-center text-sm text-[#a3a3a3]"
    >
      <div>{{ emptyHint }}</div>
      <div
        v-if="attempts.length > 0"
        class="mt-1 text-[11px] text-[#666]"
        :title="attempts.join('\n')"
      >
        已尝试 {{ attempts.length }} 个 API 路径，悬停查看详情
      </div>
    </div>

    <div
      v-else-if="visibleModels.length === 0"
      class="rounded-[6px] border border-dashed border-[#3a3a3a] bg-[#232323] px-3 py-6 text-center text-sm text-[#a3a3a3]"
    >
      {{ hideImported && importedCount > 0 ? "当前条件下的模型都已添加" : "没有匹配的模型" }}
    </div>

    <div
      v-else
      ref="listRef"
      class="model-picker-scroll min-h-0 overflow-y-auto pr-1"
      :class="compact ? 'max-h-[240px]' : 'max-h-[280px]'"
    >
      <div
        class="grid gap-1.5"
        :class="compact
          ? '[grid-template-columns:repeat(auto-fill,minmax(170px,1fr))]'
          : '[grid-template-columns:repeat(auto-fill,minmax(240px,1fr))]'"
      >
        <label
          v-for="model in visibleModels"
          :key="model.id"
          class="flex flex-col gap-1 rounded-[6px] border px-2.5 py-2 text-sm transition-colors"
          :class="[
            model.imported
              ? 'cursor-default border-[#333] bg-[#1f1f1f] text-[#6f6f6f]'
              : 'cursor-pointer',
            !model.imported && selected.has(model.id)
              ? 'border-[#1ca35a] bg-[#123322] text-white'
              : !model.imported
                ? 'border-[#3a3a3a] bg-[#232323] text-[#d4d4d4] hover:border-[#4a4a4a]'
                : '',
          ]"
        >
          <span class="center-row justify-start gap-2">
            <input
              type="checkbox"
              class="size-4 shrink-0 accent-[#10AD5D]"
              :checked="model.imported || selected.has(model.id)"
              :disabled="model.imported"
              @change="toggleModel(model)"
            />
            <span class="truncate" :title="model.id">{{ model.id }}</span>
            <span
              v-if="model.imported"
              class="ml-auto shrink-0 rounded-[4px] bg-[#2f2f2f] px-1.5 py-0.5 text-[10px] text-[#8f8f8f]"
            >
              已添加
            </span>
          </span>
          <span class="center-row min-w-0 justify-start gap-1.5 pl-6">
            <span
              class="center-row shrink-0 gap-1 rounded-[4px] border border-[#3a3a3a] px-1.5 py-[1px] text-[10px] text-[#a3a3a3]"
              :class="model.protocol.source === 'provider' ? 'opacity-60' : ''"
            >
              <span :class="[model.badge.icon, 'text-[11px]']"></span>
              <span>{{ model.badge.text }}</span>
            </span>
            <span class="truncate text-[11px] text-[#737373]" :title="model.previewName">
              {{ model.previewName }}
            </span>
          </span>
        </label>
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-picker-scroll {
  scrollbar-width: thin;
  scrollbar-color: #4a4a4a transparent;
}

.model-picker-scroll::-webkit-scrollbar {
  width: 6px;
}

.model-picker-scroll::-webkit-scrollbar-thumb {
  border-radius: 999px;
  background: #4a4a4a;
}
</style>