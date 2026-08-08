<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  disconnectCursorAccount,
  getCursorAccountStatus,
  startCursorAccountLogin,
} from "@/services/clientApi";
import { toUserError } from "@/state/appState";
import { Browser } from "@wailsio/runtime";
import { computed, onMounted, onUnmounted, ref } from "vue";

const CONTRIBUTOR_URL = "https://github.com/aike0210";
const STATUS_POLL_INTERVAL_MS = 1500;

const message = useMessage();

const accountStatus = ref({
  state: "signed_out",
  authId: "",
  email: "",
  error: "",
});
const busy = ref(false);
let pollTimer = null;

// 卡片只展示脱敏后的标识：完整邮箱或 authId 属于账号信息，没有必要常驻在首页。
function maskIdentifier(value) {
  const identifier = String(value || "").trim();
  if (!identifier) return "";

  const atIndex = identifier.indexOf("@");
  if (atIndex > 0 && atIndex < identifier.length - 1) {
    const localPart = identifier.slice(0, atIndex);
    const domain = identifier.slice(atIndex + 1);
    const maskedLocalPart =
      localPart.length <= 2 ? `${localPart[0]}***` : `${localPart[0]}***${localPart.at(-1)}`;
    return `${maskedLocalPart}@${domain}`;
  }

  if (identifier.length <= 8) return "****";
  return `${identifier.slice(0, 4)}****${identifier.slice(-4)}`;
}

const signedIn = computed(() => accountStatus.value.state === "signed_in");
const waiting = computed(() => accountStatus.value.state === "waiting");
const displayIdentifier = computed(() => {
  if (!signedIn.value) return "";
  return maskIdentifier(accountStatus.value.email || accountStatus.value.authId);
});
const stateText = computed(() => {
  if (signedIn.value) return "已经登录";
  if (waiting.value) return "等待浏览器登录";
  return "未连接";
});

function showActionError(title, error) {
  const detail = String(error || "").trim();
  message.error(detail ? `${title}：${detail}` : title);
}

async function handleOpenContributor() {
  try {
    await Browser.OpenURL(CONTRIBUTOR_URL);
  } catch (error) {
    showActionError("打开贡献者主页失败", toUserError(error));
  }
}

async function refreshStatus() {
  accountStatus.value = await getCursorAccountStatus();
}

async function handleLogin() {
  busy.value = true;
  try {
    accountStatus.value = await startCursorAccountLogin();
  } catch (error) {
    showActionError("登录失败", toUserError(error));
    await refreshStatus().catch(() => {});
  } finally {
    busy.value = false;
  }
}

async function handleDisconnect() {
  const confirmed = await showModal({
    title: "退出登录",
    content: "只会退出本应用中的 Cursor 账号，不会退出 Cursor 客户端。是否继续？",
    confirmText: "退出登录",
    showCancel: true,
  });
  if (!confirmed) return;

  busy.value = true;
  try {
    accountStatus.value = await disconnectCursorAccount();
  } catch (error) {
    showActionError("退出登录失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

onMounted(async () => {
  await refreshStatus().catch(() => {});
  // 浏览器授权完成没有回调通知桌面端，只在等待态轮询，避免常态空转。
  pollTimer = window.setInterval(() => {
    if (waiting.value) {
      void refreshStatus().catch(() => {});
    }
  }, STATUS_POLL_INTERVAL_MS);
});

onUnmounted(() => {
  if (pollTimer) {
    window.clearInterval(pollTimer);
    pollTimer = null;
  }
});
</script>

<template>
  <Card>
    <div class="flex flex-col gap-3">
      <div class="flex items-center justify-between gap-4">
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <h2 class="text-base font-medium text-white">Cursor 控制面账号</h2>
          <span
            class="rounded-full border border-[#3a3a3a] bg-[#202020] px-2 py-0.5 text-xs text-[#b8b8b8]"
          >
            {{ stateText }}
          </span>
        </div>
        <div class="flex shrink-0 items-center gap-1 text-xs text-[#737373]">
          <span>@aike0210</span>
          <Tooltip>
            <div class="flex min-w-[220px] flex-col gap-2">
              <div>感谢 @aike0210 对 Cursor 控制面账号功能的贡献。</div>
              <button
                type="button"
                class="flex items-center gap-2 text-left text-[#8ab4f8] transition-colors duration-150 hover:text-[#b6d0fb]"
                @click="handleOpenContributor"
              >
                <span class="icon-[mdi--github] text-[14px]"></span>
                <span>github.com/aike0210</span>
                <span class="icon-[mdi--open-in-new] text-[12px]"></span>
              </button>
            </div>
          </Tooltip>
        </div>
      </div>

      <div class="flex items-end justify-between gap-4">
        <div class="min-w-0">
          <div v-if="displayIdentifier" class="truncate text-sm text-[#d0d0d0]">
            {{ displayIdentifier }}
          </div>
          <div class="mt-1 text-sm text-[#a3a3a3]">
            独立用于插件、Skills 和 MCP；不会改变 Cursor 客户端当前账号
          </div>
          <div v-if="waiting" class="mt-1 text-sm text-[#d6a84b]">
            请在浏览器完成登录，完成后返回 Cursor 重新打开插件市场
          </div>
          <div v-if="accountStatus.error" class="mt-1 break-all text-sm text-[#e06c75]">
            {{ accountStatus.error }}
          </div>
        </div>
        <Button
          v-if="signedIn"
          class="shrink-0"
          :disabled="busy"
          :loading="busy"
          @click="handleDisconnect"
        >
          退出登录
        </Button>
        <Button
          v-else
          class="shrink-0"
          variant="primary"
          :disabled="busy || waiting"
          :loading="busy"
          @click="handleLogin"
        >
          {{ waiting ? "等待登录..." : "登录 Cursor" }}
        </Button>
      </div>
    </div>
  </Card>
</template>