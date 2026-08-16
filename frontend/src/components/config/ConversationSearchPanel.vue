<script setup>
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import Switch from "@/components/ui/Switch.vue";
import {
  deleteConversation,
  exportConversationMarkdown,
  getConversationTranscript,
  searchConversations,
} from "@/services/clientApi";
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";

const emit = defineEmits(["confirm-action"]);

const query = ref("");
const includeTools = ref(false);
const mode = ref("");
const rangeDays = ref(0);
const loading = ref(false);
const error = ref("");
const hits = ref([]);
const truncated = ref(false);
const searchedOnce = ref(false);

const view = ref("list");
const transcript = ref(null);
const transcriptLoading = ref(false);
const expandedTools = ref(new Set());
const exporting = ref(false);
const exportPath = ref("");
const detailScrollRef = ref(null);
const currentHitIndex = ref(-1);
const copiedSeq = ref(0);
let copiedTimer = null;

const rangeOptions = [
  { label: "全部时间", value: 0 },
  { label: "24 小时内", value: 1 },
  { label: "最近 7 天", value: 7 },
  { label: "最近 30 天", value: 30 },
];

const modeOptions = computed(() => {
  const seen = new Set(hits.value.map((item) => String(item?.mode || "")).filter(Boolean));
  if (mode.value) seen.add(mode.value);
  return [
    { label: "全部模式", value: "" },
    ...[...seen].sort().map((value) => ({ label: value, value })),
  ];
});

let debounceTimer = null;
let searchToken = 0;

async function runSearch() {
  const token = ++searchToken;
  loading.value = true;
  error.value = "";
  try {
    const result = await searchConversations({
      query: query.value.trim(),
      includeTools: includeTools.value,
      mode: mode.value,
      updatedWithinDays: Number(rangeDays.value) || 0,
    });
    if (token !== searchToken) return;
    hits.value = Array.isArray(result?.hits) ? result.hits : [];
    truncated.value = Boolean(result?.truncated);
    searchedOnce.value = true;
  } catch (cause) {
    if (token !== searchToken) return;
    error.value = String(cause?.message || cause || "会话搜索失败");
  } finally {
    if (token === searchToken) {
      loading.value = false;
    }
  }
}

function scheduleSearch() {
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    debounceTimer = null;
    void runSearch();
  }, 400);
}

watch(query, scheduleSearch);
watch([includeTools, mode, rangeDays], () => void runSearch());

function currentKeywords() {
  return query.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
}

function messageMatchesKeywords(message, keywords) {
  if (keywords.length === 0) return false;
  const combined = [message.text, message.arguments, message.resultText]
    .filter(Boolean)
    .join("\n")
    .toLowerCase();
  return keywords.some((keyword) => combined.includes(keyword));
}

// 详情中所有命中当前关键词的消息 seq，驱动命中导航条。
const hitSeqs = computed(() => {
  const keywords = currentKeywords();
  const messages = transcript.value?.messages || [];
  if (keywords.length === 0) return [];
  return messages.filter((message) => messageMatchesKeywords(message, keywords)).map((message) => message.seq);
});

const currentHitSeq = computed(() => {
  const seqs = hitSeqs.value;
  if (currentHitIndex.value < 0 || currentHitIndex.value >= seqs.length) return 0;
  return seqs[currentHitIndex.value];
});

// 整句组装，翻译时可整体调整语序。
const hitPositionText = computed(() => `第 ${currentHitIndex.value + 1} / ${hitSeqs.value.length} 条命中`);

async function openConversation(conversationID, targetSeq = 0) {
  view.value = "detail";
  transcriptLoading.value = true;
  error.value = "";
  exportPath.value = "";
  expandedTools.value = new Set();
  currentHitIndex.value = -1;
  try {
    transcript.value = await getConversationTranscript(conversationID);
    await nextTick();
    if (targetSeq > 0) {
      // 从片段点进来：直接定位那条消息；若它是命中之一，同步导航索引。
      const index = hitSeqs.value.indexOf(targetSeq);
      if (index >= 0) currentHitIndex.value = index;
      scrollToSeq(targetSeq);
    } else if (hitSeqs.value.length > 0) {
      currentHitIndex.value = 0;
      scrollToSeq(hitSeqs.value[0]);
    }
  } catch (cause) {
    error.value = String(cause?.message || cause || "会话读取失败");
    view.value = "list";
  } finally {
    transcriptLoading.value = false;
  }
}

function scrollToSeq(seq) {
  const container = detailScrollRef.value;
  const target = container?.querySelector(`[data-seq="${seq}"]`);
  target?.scrollIntoView({ block: "start" });
}

function goToHit(delta) {
  const seqs = hitSeqs.value;
  if (seqs.length === 0) return;
  let next = currentHitIndex.value + delta;
  if (next < 0) next = seqs.length - 1;
  if (next >= seqs.length) next = 0;
  currentHitIndex.value = next;
  scrollToSeq(seqs[next]);
}

function backToList() {
  view.value = "list";
  transcript.value = null;
  exportPath.value = "";
  currentHitIndex.value = -1;
}

function handleKeydown(event) {
  if (event.key === "Escape" && view.value === "detail") {
    backToList();
  }
}

async function exportMarkdown() {
  if (!transcript.value) return;
  exporting.value = true;
  error.value = "";
  try {
    const path = await exportConversationMarkdown(transcript.value.conversationId);
    // 空路径表示用户取消了保存对话框，不需要提示。
    exportPath.value = String(path || "");
  } catch (cause) {
    error.value = String(cause?.message || cause || "导出失败");
  } finally {
    exporting.value = false;
  }
}

function requestDelete(item) {
  const title = conversationTitle(item);
  emit("confirm-action", {
    title: "删除会话",
    content: `将永久删除会话「${title}」的全部历史与调试记录，不可恢复。确定继续？`,
    failureTitle: "删除失败",
    run: async () => {
      try {
        await deleteConversation(item.conversationId);
        await runSearch();
        return { ok: true };
      } catch (cause) {
        return { ok: false, error: String(cause?.message || cause || "服务错误") };
      }
    },
  });
}

async function copyMessage(message) {
  const text = message.text || [message.arguments, message.resultText].filter(Boolean).join("\n");
  try {
    await navigator.clipboard.writeText(text);
    copiedSeq.value = message.seq;
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => {
      copiedSeq.value = 0;
      copiedTimer = null;
    }, 1500);
  } catch (_cause) {
    // 剪贴板不可用时静默失败，不打断阅读。
  }
}

function toggleToolExpanded(seq) {
  const next = new Set(expandedTools.value);
  if (next.has(seq)) {
    next.delete(seq);
  } else {
    next.add(seq);
  }
  expandedTools.value = next;
}

const TOOL_PREVIEW_LIMIT = 400;

function toolResultPreview(message) {
  const text = String(message.resultText || "");
  if (expandedTools.value.has(message.seq) || text.length <= TOOL_PREVIEW_LIMIT) {
    return text;
  }
  return text.slice(0, TOOL_PREVIEW_LIMIT);
}

function toolResultTruncated(message) {
  return String(message.resultText || "").length > TOOL_PREVIEW_LIMIT;
}

// 关键词高亮：把文本按命中词切分成段落，命中段加高亮样式；不用 v-html，避免注入。
function highlightSegments(text) {
  const keywords = currentKeywords();
  const source = String(text || "");
  if (keywords.length === 0 || source === "") {
    return [{ text: source, hit: false }];
  }
  const lower = source.toLowerCase();
  const segments = [];
  let cursor = 0;
  while (cursor < source.length) {
    let bestIndex = -1;
    let bestLength = 0;
    for (const keyword of keywords) {
      const index = lower.indexOf(keyword, cursor);
      if (index >= 0 && (bestIndex < 0 || index < bestIndex)) {
        bestIndex = index;
        bestLength = keyword.length;
      }
    }
    if (bestIndex < 0) {
      segments.push({ text: source.slice(cursor), hit: false });
      break;
    }
    if (bestIndex > cursor) {
      segments.push({ text: source.slice(cursor, bestIndex), hit: false });
    }
    segments.push({ text: source.slice(bestIndex, bestIndex + bestLength), hit: true });
    cursor = bestIndex + bestLength;
  }
  return segments;
}

// 相对时间：今天/昨天显示到分钟，今年内显示月日，更早带年份，扫一眼即知远近。
function formatRelativeTime(value) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getFullYear() <= 1) return "-";
  const now = new Date();
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const startOfThatDay = new Date(date.getFullYear(), date.getMonth(), date.getDate());
  const dayDiff = Math.round((startOfToday - startOfThatDay) / 86400000);
  const clock = `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
  if (dayDiff === 0) return `今天 ${clock}`;
  if (dayDiff === 1) return `昨天 ${clock}`;
  if (date.getFullYear() === now.getFullYear()) return `${date.getMonth() + 1}月${date.getDate()}日 ${clock}`;
  return `${date.getFullYear()}/${date.getMonth() + 1}/${date.getDate()}`;
}

function formatTime(value) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getFullYear() <= 1) return "-";
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function conversationTitle(item) {
  return String(item?.name || "").trim() || String(item?.conversationId || "").slice(0, 12);
}

function matchKindLabel(kind) {
  switch (kind) {
    case "user":
      return "用户";
    case "assistant":
      return "助手";
    case "tool":
      return "工具";
    case "summary":
      return "摘要";
    default:
      return kind;
  }
}

onMounted(() => {
  void runSearch();
});
onActivated(() => {
  window.addEventListener("keydown", handleKeydown);
  // 列表模式下回到面板时刷新，会话可能刚产生了新内容；详情模式保持阅读位置。
  if (view.value === "list") {
    void runSearch();
  }
});
onDeactivated(() => {
  window.removeEventListener("keydown", handleKeydown);
  // KeepAlive 下切走 tab 不触发 unmount，这里同样要停掉待触发的防抖搜索。
  if (debounceTimer) {
    clearTimeout(debounceTimer);
    debounceTimer = null;
  }
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", handleKeydown);
  if (debounceTimer) clearTimeout(debounceTimer);
  if (copiedTimer) clearTimeout(copiedTimer);
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-3 overflow-hidden">
    <!-- 列表模式 -->
    <template v-if="view === 'list'">
      <div class="flex flex-wrap items-center gap-3 rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2.5">
        <input
          v-model="query"
          class="conversation-search-input min-w-[220px] flex-1"
          placeholder="搜索历史会话，多个关键词用空格分隔"
          @keyup.enter="runSearch"
        />
        <Select v-model="rangeDays" class="w-[130px]" :options="rangeOptions" aria-label="时间范围" />
        <Select v-model="mode" class="w-[130px]" :options="modeOptions" aria-label="会话模式" />
        <Switch compact :enabled="includeTools" label="包含工具输出" @change="(value) => (includeTools = value)" />
        <Button variant="primary" :loading="loading" @click="runSearch">搜索</Button>
      </div>

      <div v-if="error" class="rounded-[7px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]">
        {{ error }}
      </div>
      <div v-if="truncated" class="rounded-[7px] border border-[#5a4314] bg-[#2b2413] px-3 py-2 text-xs text-[#fcd34d]">
        结果过多已截断，可加关键词或缩小时间范围
      </div>

      <div class="min-h-0 flex-1 overflow-auto rounded-[8px] border border-[#343434] bg-[#171717]">
        <div class="sticky top-0 z-10 flex items-center justify-between border-b border-[#303030] bg-[#202020] px-3 py-2 text-xs text-[#8f8f8f]">
          <span v-if="query.trim()">命中 {{ hits.length }} 个会话</span>
          <span v-else>最近 {{ hits.length }} 个会话</span>
          <span>按更新时间排序</span>
        </div>
        <div v-if="!loading && searchedOnce && hits.length === 0" class="py-12 text-center text-sm text-[#737373]">
          没有匹配的会话
        </div>
        <div
          v-for="item in hits"
          :key="item.conversationId"
          class="group cursor-pointer border-b border-[#262626] px-3 py-2.5 transition-colors duration-100 hover:bg-[#202020]"
          @click="openConversation(item.conversationId)"
        >
          <div class="flex items-center justify-between gap-3">
            <span class="min-w-0 truncate text-sm text-[#e5e5e5]">
              {{ conversationTitle(item) }}
              <span v-if="item.titleMatched" class="ml-1 rounded-sm bg-[#123322] px-1 text-[10px] text-[#4ade80]">标题命中</span>
            </span>
            <span class="center-row shrink-0 gap-2">
              <span class="text-xs text-[#737373]">{{ formatRelativeTime(item.updatedAt) }}</span>
              <button
                type="button"
                class="rounded-[5px] border border-transparent px-1.5 py-0.5 text-xs text-[#737373] opacity-0 transition-opacity duration-100 hover:border-[#4b1d1d] hover:bg-[#2a1313] hover:text-[#fca5a5] group-hover:opacity-100"
                @click.stop="requestDelete(item)"
              >
                删除
              </button>
            </span>
          </div>
          <div class="mt-0.5 flex items-center gap-2 text-xs text-[#737373]">
            <span>{{ item.mode }}</span>
            <span v-if="item.entryCount > 0">{{ item.entryCount }} 条记录</span>
            <span v-if="item.totalMatches > 0" class="text-[#4ade80]">{{ item.totalMatches }} 条命中</span>
          </div>
          <div
            v-for="match in item.matches || []"
            :key="match.seq"
            class="mt-1 cursor-pointer truncate rounded-[4px] px-1 py-0.5 font-mono text-xs text-[#a3a3a3] transition-colors duration-100 hover:bg-[#262626] hover:text-[#d4d4d4]"
            title="点击定位到这条消息"
            @click.stop="openConversation(item.conversationId, match.seq)"
          >
            <span class="mr-1 rounded-sm bg-[#2a2a2a] px-1 text-[10px] text-[#8f8f8f]">{{ matchKindLabel(match.kind) }}</span>
            <span v-for="(segment, index) in highlightSegments(match.snippet)" :key="index" :class="segment.hit ? 'rounded-sm bg-[#1c4a2e] px-0.5 text-[#4ade80]' : ''">{{ segment.text }}</span>
          </div>
        </div>
      </div>
    </template>

    <!-- 详情模式 -->
    <template v-else>
      <div class="flex flex-wrap items-center justify-between gap-3 rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2.5">
        <div class="center-row min-w-0 gap-2">
          <Button variant="default" title="Esc" @click="backToList">返回</Button>
          <span class="truncate text-sm text-[#e5e5e5]">{{ conversationTitle(transcript) }}</span>
        </div>
        <div class="center-row shrink-0 gap-2">
          <template v-if="hitSeqs.length > 0">
            <span class="text-xs text-[#8f8f8f]">{{ hitPositionText }}</span>
            <Button variant="default" @click="goToHit(-1)">
              <span class="icon-[mdi--chevron-up] text-[16px]"></span>
            </Button>
            <Button variant="default" @click="goToHit(1)">
              <span class="icon-[mdi--chevron-down] text-[16px]"></span>
            </Button>
          </template>
          <Button variant="primary" :loading="exporting" :disabled="transcriptLoading" @click="exportMarkdown">导出 Markdown</Button>
        </div>
      </div>

      <div v-if="error" class="rounded-[7px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]">
        {{ error }}
      </div>
      <div v-if="exportPath" class="truncate rounded-[7px] border border-[#14532d] bg-[#102418] px-3 py-2 text-xs text-[#86efac]" :title="exportPath">
        已导出：{{ exportPath }}
      </div>

      <div ref="detailScrollRef" class="min-h-0 flex-1 overflow-auto rounded-[8px] border border-[#343434] bg-[#171717] px-3 py-2">
        <div v-if="transcriptLoading" class="py-12 text-center text-sm text-[#737373]">正在加载会话…</div>
        <template v-else-if="transcript">
          <div class="border-b border-[#262626] pb-2 text-xs text-[#737373]">
            <span>创建于 {{ formatTime(transcript.createdAt) }}</span>
            <span class="mx-2">·</span>
            <span>更新于 {{ formatTime(transcript.updatedAt) }}</span>
            <span class="mx-2">·</span>
            <span>{{ (transcript.messages || []).length }} 条消息</span>
          </div>
          <div
            v-for="message in transcript.messages || []"
            :key="message.seq"
            :data-seq="message.seq"
            class="group relative border-b border-[#222222] py-2.5 pl-2"
            :class="message.seq === currentHitSeq ? 'border-l-2 border-l-[#4ade80] bg-[#1a2018]' : 'border-l-2 border-l-transparent'"
          >
            <button
              type="button"
              class="absolute right-1 top-2 rounded-[5px] border border-[#3a3a3a] bg-[#252525] px-1.5 py-0.5 text-[10px] text-[#8f8f8f] opacity-0 transition-opacity duration-100 hover:text-[#e5e5e5] group-hover:opacity-100"
              @click="copyMessage(message)"
            >
              {{ copiedSeq === message.seq ? "已复制" : "复制" }}
            </button>
            <template v-if="message.kind === 'user'">
              <div class="mb-1 text-xs font-medium text-[#4ade80]">用户 · {{ formatTime(message.createdAt) }}</div>
              <div class="whitespace-pre-wrap break-words text-sm leading-relaxed text-[#e5e5e5]"><span v-for="(segment, index) in highlightSegments(message.text)" :key="index" :class="segment.hit ? 'rounded-sm bg-[#1c4a2e] px-0.5 text-[#4ade80]' : ''">{{ segment.text }}</span></div>
            </template>
            <template v-else-if="message.kind === 'assistant'">
              <div class="mb-1 text-xs font-medium text-[#93c5fd]">助手</div>
              <div class="whitespace-pre-wrap break-words text-sm leading-relaxed text-[#cfcfcf]"><span v-for="(segment, index) in highlightSegments(message.text)" :key="index" :class="segment.hit ? 'rounded-sm bg-[#1c4a2e] px-0.5 text-[#4ade80]' : ''">{{ segment.text }}</span></div>
            </template>
            <template v-else-if="message.kind === 'summary'">
              <div class="mb-1 text-xs font-medium text-[#fcd34d]">历史压缩摘要</div>
              <div class="whitespace-pre-wrap break-words text-xs leading-relaxed text-[#a3a3a3]"><span v-for="(segment, index) in highlightSegments(message.text)" :key="index" :class="segment.hit ? 'rounded-sm bg-[#1c4a2e] px-0.5 text-[#4ade80]' : ''">{{ segment.text }}</span></div>
            </template>
            <template v-else-if="message.kind === 'tool'">
              <div class="mb-1 text-xs font-medium text-[#8f8f8f]">工具 · {{ message.toolName }}</div>
              <div v-if="message.arguments" class="mb-1 truncate font-mono text-xs text-[#737373]" :title="message.arguments">
                {{ message.arguments }}
              </div>
              <div v-if="message.resultText" class="whitespace-pre-wrap break-words rounded-[6px] bg-[#1d1d1d] px-2 py-1.5 font-mono text-xs leading-relaxed text-[#a3a3a3]">{{ toolResultPreview(message) }}<template v-if="toolResultTruncated(message) && !expandedTools.has(message.seq)">…</template></div>
              <button
                v-if="toolResultTruncated(message)"
                type="button"
                class="mt-1 text-xs text-[#1ca35a] hover:underline"
                @click="toggleToolExpanded(message.seq)"
              >
                {{ expandedTools.has(message.seq) ? "收起" : "展开全部" }}
              </button>
            </template>
          </div>
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
.conversation-search-input {
  border: 1px solid #3a3a3a;
  border-radius: 7px;
  background: #202020;
  padding: 7px 10px;
  color: #e5e5e5;
  outline: none;
}

.conversation-search-input:focus {
  border-color: #1ca35a;
}
</style>
