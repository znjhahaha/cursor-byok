<script setup>
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import { exportDiagnosticBundle, getDiagnosticLogs, openLogsDirectory } from "@/services/clientApi";
import { appState, saveDetailedLogging } from "@/state/appState";
import { computed, onActivated, onMounted, ref } from "vue";

const levelOptions = [
  { label: "全部级别", value: "ALL" },
  { label: "错误", value: "ERROR" },
  { label: "警告", value: "WARN" },
  { label: "信息", value: "INFO" },
  { label: "调试", value: "DEBUG" },
];

const level = ref("ALL");
const requestID = ref("");
const model = ref("");
const search = ref("");
const entries = ref([]);
const total = ref(0);
const nextOffset = ref(0);
const hasMore = ref(false);
const loading = ref(false);
const saving = ref(false);
const exporting = ref(false);
const error = ref("");
const exportPath = ref("");

const query = computed(() => ({
  offset: 0,
  limit: 200,
  level: level.value,
  requestID: requestID.value,
  model: model.value,
  search: search.value,
}));

async function refresh() {
  loading.value = true;
  error.value = "";
  try {
    const page = await getDiagnosticLogs(query.value);
    entries.value = Array.isArray(page?.entries) ? page.entries : [];
    total.value = Number(page?.total || 0);
    nextOffset.value = Number(page?.nextOffset || entries.value.length);
    hasMore.value = Boolean(page?.hasMore);
  } catch (cause) {
    error.value = String(cause?.message || cause || "日志读取失败");
  } finally {
    loading.value = false;
  }
}

async function loadMore() {
  if (!hasMore.value || loading.value) return;
  loading.value = true;
  try {
    const page = await getDiagnosticLogs({ ...query.value, offset: nextOffset.value });
    entries.value.push(...(Array.isArray(page?.entries) ? page.entries : []));
    nextOffset.value = Number(page?.nextOffset || entries.value.length);
    hasMore.value = Boolean(page?.hasMore);
  } catch (cause) {
    error.value = String(cause?.message || cause || "日志读取失败");
  } finally {
    loading.value = false;
  }
}

async function toggleLogging(event) {
  saving.value = true;
  const result = await saveDetailedLogging(event.target.checked);
  saving.value = false;
  if (!result?.ok) {
    error.value = result?.error || "日志设置保存失败";
  }
}

async function copySummary() {
  const text = entries.value.map((item) => item.message).join("\n");
  await navigator.clipboard.writeText(text);
}

async function exportBundle() {
  exporting.value = true;
  error.value = "";
  try {
    const result = await exportDiagnosticBundle();
    exportPath.value = String(result?.path || "");
  } catch (cause) {
    error.value = String(cause?.message || cause || "诊断包导出失败");
  } finally {
    exporting.value = false;
  }
}

onMounted(refresh);
onActivated(refresh);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-3 overflow-hidden">
    <div class="flex flex-wrap items-center justify-between gap-3 rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2.5">
      <label class="center-row gap-2 text-sm text-[#d4d4d4]">
        <input
          type="checkbox"
          class="h-4 w-4 accent-[#1ca35a]"
          :checked="appState.log"
          :disabled="saving"
          @change="toggleLogging"
        />
        详细日志（热加载）
      </label>
      <div class="center-row flex-wrap gap-2">
        <Button variant="default" @click="openLogsDirectory">打开目录</Button>
        <Button variant="default" :disabled="entries.length === 0" @click="copySummary">复制摘要</Button>
        <Button variant="primary" :loading="exporting" @click="exportBundle">导出诊断包</Button>
      </div>
    </div>

    <div class="rounded-[8px] border border-[#14532d] bg-[#102418] px-3 py-2 text-xs leading-relaxed text-[#86efac]">
      查看器和诊断包会强制隐藏 Authorization、Cookie、API Key、Token、密码与完整请求正文；原始 Bidi 数据不会导出。
    </div>

    <div class="grid grid-cols-1 gap-2 rounded-[8px] border border-[#343434] bg-[#252525] p-3 sm:grid-cols-2 xl:grid-cols-5">
      <Select v-model="level" :options="levelOptions" aria-label="日志级别" />
      <input v-model.trim="requestID" class="diagnostic-input" placeholder="请求 ID" />
      <input v-model.trim="model" class="diagnostic-input" placeholder="模型或渠道" />
      <input v-model.trim="search" class="diagnostic-input" placeholder="关键词" @keyup.enter="refresh" />
      <Button variant="default" :loading="loading" @click="refresh">刷新</Button>
    </div>

    <div v-if="error" class="rounded-[7px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]">
      {{ error }}
    </div>
    <div v-if="exportPath" class="truncate rounded-[7px] border border-[#5a4314] bg-[#2b2413] px-3 py-2 text-xs text-[#fcd34d]" :title="exportPath">
      已导出：{{ exportPath }}
    </div>

    <div class="min-h-0 flex-1 overflow-auto rounded-[8px] border border-[#343434] bg-[#171717]">
      <div class="sticky top-0 z-10 flex items-center justify-between border-b border-[#303030] bg-[#202020] px-3 py-2 text-xs text-[#8f8f8f]">
        <span>已显示 {{ entries.length }} / {{ total }} 条</span>
        <span>最新日志优先</span>
      </div>
      <div v-if="!loading && entries.length === 0" class="py-12 text-center text-sm text-[#737373]">暂无匹配日志</div>
      <div v-for="entry in entries" :key="entry.index" class="grid grid-cols-[58px_minmax(0,1fr)] gap-2 border-b border-[#262626] px-3 py-2 font-mono text-xs">
        <span
          :class="entry.level === 'ERROR' ? 'text-[#fca5a5]' : entry.level === 'WARN' ? 'text-[#fcd34d]' : entry.level === 'DEBUG' ? 'text-[#67e8f9]' : 'text-[#86efac]'"
        >
          {{ entry.level }}
        </span>
        <span class="whitespace-pre-wrap break-all text-[#cfcfcf]">{{ entry.message }}</span>
      </div>
      <div v-if="hasMore" class="p-3 text-center">
        <Button variant="default" :loading="loading" @click="loadMore">加载更多</Button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.diagnostic-input {
  min-width: 0;
  border: 1px solid #3a3a3a;
  border-radius: 7px;
  background: #202020;
  padding: 7px 10px;
  color: #e5e5e5;
  outline: none;
}

.diagnostic-input:focus {
  border-color: #1ca35a;
}
</style>
