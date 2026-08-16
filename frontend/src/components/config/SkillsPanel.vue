<script setup>
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import {
  addSkillRepo,
  backupSkillsToZip,
  checkSkillUpdates,
  fetchRemoteSkills,
  getInstalledSkillContent,
  getRemoteSkillContent,
  installSkill,
  listInstalledSkills,
  listSkillRepos,
  openSkillsDirectory,
  removeSkillRepo,
  restoreSkillsFromZip,
  uninstallSkill,
} from "@/services/clientApi";
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";

const emit = defineEmits(["confirm-action"]);

const activeTab = ref("browse");
const installed = ref([]);
const installedLoading = ref(false);
const installedFilter = ref("");
const repos = ref([]);
const selectedRepo = ref("anthropics/skills");
const remote = ref([]);
const remoteLoading = ref(false);
const remoteFilter = ref("");
const fetchedRepo = ref("");
const newRepoSpec = ref("");
const addingRepo = ref(false);
const addRepoDialog = ref(false);
const addRepoError = ref("");
const addRepoInput = ref(null);
const installingSubdirs = ref(new Set());
const updatingNames = ref(new Set());
const backingUp = ref(false);
const checkingUpdates = ref(false);
// dirName -> SkillUpdateStatus，检查更新后用于给已安装卡片打"有更新"徽标。
const updateStatuses = ref({});
const updatingAll = ref(false);
const error = ref("");
const notice = ref("");
const preview = ref(null);

const repoOptions = computed(() =>
  repos.value.map((repo) => ({
    label: repo.builtIn ? `${repo.id}（内置）` : repo.id,
    value: repo.id,
  })),
);

const selectedRepoRemovable = computed(() => {
  const repo = repos.value.find((item) => item.id === selectedRepo.value);
  return Boolean(repo && !repo.builtIn);
});

function matchesFilter(skill, filter) {
  const needle = filter.trim().toLowerCase();
  if (!needle) return true;
  return [skill.name, skill.description, skill.subdir, skill.sourceRepo]
    .filter(Boolean)
    .join("\n")
    .toLowerCase()
    .includes(needle);
}

const filteredInstalled = computed(() => installed.value.filter((skill) => matchesFilter(skill, installedFilter.value)));
const filteredRemote = computed(() => remote.value.filter((skill) => matchesFilter(skill, remoteFilter.value)));

async function refreshInstalled() {
  installedLoading.value = true;
  try {
    const result = await listInstalledSkills();
    installed.value = Array.isArray(result) ? result : [];
  } catch (cause) {
    error.value = String(cause?.message || cause || "读取已安装 skills 失败");
  } finally {
    installedLoading.value = false;
  }
}

async function refreshRepos() {
  try {
    const result = await listSkillRepos();
    repos.value = Array.isArray(result) ? result : [];
    if (!repos.value.some((repo) => repo.id === selectedRepo.value) && repos.value.length > 0) {
      selectedRepo.value = repos.value[0].id;
    }
  } catch (cause) {
    error.value = String(cause?.message || cause || "读取仓库列表失败");
  }
}

async function fetchRemote(refresh) {
  if (!selectedRepo.value) return;
  const repoID = selectedRepo.value;
  remoteLoading.value = true;
  error.value = "";
  notice.value = "";
  try {
    const result = await fetchRemoteSkills(repoID, Boolean(refresh));
    // 请求期间用户可能已切换仓库，过期结果直接丢弃。
    if (selectedRepo.value !== repoID) return;
    remote.value = Array.isArray(result) ? result : [];
    fetchedRepo.value = repoID;
    if (remote.value.length === 0) {
      notice.value = "该仓库中没有发现 SKILL.md";
    }
  } catch (cause) {
    if (selectedRepo.value === repoID) {
      error.value = String(cause?.message || cause || "拉取 skill 列表失败");
    }
  } finally {
    if (selectedRepo.value === repoID) {
      remoteLoading.value = false;
    }
  }
}

// 选中仓库即自动拉取，省掉手动点按钮的一步。
watch(selectedRepo, (repoID) => {
  if (repoID && repoID !== fetchedRepo.value) {
    remote.value = [];
    void fetchRemote(false);
  }
});

function openAddRepo() {
  addRepoError.value = "";
  newRepoSpec.value = "";
  addRepoDialog.value = true;
  void nextTick(() => addRepoInput.value?.focus());
}

function closeAddRepo() {
  addRepoDialog.value = false;
  addRepoError.value = "";
}

async function addRepo() {
  const spec = newRepoSpec.value.trim();
  if (!spec) {
    addRepoError.value = "请输入仓库地址";
    return;
  }
  addingRepo.value = true;
  addRepoError.value = "";
  try {
    const repo = await addSkillRepo(spec);
    newRepoSpec.value = "";
    await refreshRepos();
    if (repo?.id) {
      selectedRepo.value = repo.id;
    }
    addRepoDialog.value = false;
    notice.value = `已添加仓库 ${repo?.id || spec}`;
  } catch (cause) {
    addRepoError.value = String(cause?.message || cause || "添加仓库失败");
  } finally {
    addingRepo.value = false;
  }
}

async function removeSelectedRepo() {
  if (!selectedRepoRemovable.value) return;
  try {
    await removeSkillRepo(selectedRepo.value);
    if (fetchedRepo.value === selectedRepo.value) {
      remote.value = [];
      fetchedRepo.value = "";
    }
    await refreshRepos();
  } catch (cause) {
    error.value = String(cause?.message || cause || "删除仓库失败");
  }
}

async function install(skill) {
  const key = skill.subdir || "@root";
  const next = new Set(installingSubdirs.value);
  next.add(key);
  installingSubdirs.value = next;
  error.value = "";
  notice.value = "";
  try {
    const result = await installSkill(fetchedRepo.value || selectedRepo.value, skill.subdir);
    notice.value = `已安装「${result?.name || skill.name}」，Cursor 下次对话即可使用`;
    await refreshInstalled();
    markRemoteInstalled();
    // 刚装的就是仓库当前版本，清掉"有更新"标记。
    remote.value = remote.value.map((item) =>
      (item.subdir || "@root") === key ? { ...item, updateAvailable: false } : item,
    );
    clearUpdateStatus(installedDirNameFromRemote(skill));
  } catch (cause) {
    error.value = String(cause?.message || cause || "安装失败");
  } finally {
    const done = new Set(installingSubdirs.value);
    done.delete(key);
    installingSubdirs.value = done;
  }
}

// 已安装 skill 带来源记录时可一键按来源覆盖重装，等价于"更新到仓库最新版"。
async function updateInstalled(skill) {
  const dirName = installedDirName(skill);
  const next = new Set(updatingNames.value);
  next.add(dirName);
  updatingNames.value = next;
  error.value = "";
  notice.value = "";
  try {
    const result = await installSkill(skill.sourceRepo, skill.sourcePath || "");
    notice.value = `已更新「${result?.name || skill.name}」到仓库最新版本`;
    await refreshInstalled();
    markRemoteInstalled();
    clearUpdateStatus(dirName);
  } catch (cause) {
    error.value = String(cause?.message || cause || "更新失败");
  } finally {
    const done = new Set(updatingNames.value);
    done.delete(dirName);
    updatingNames.value = done;
  }
}

// clearUpdateStatus 在安装/更新成功后移除该 skill 的"有更新"标记。
function clearUpdateStatus(dirName) {
  if (!dirName || !updateStatuses.value[dirName]) return;
  const next = { ...updateStatuses.value };
  delete next[dirName];
  updateStatuses.value = next;
}

// installedDirNameFromRemote 按远端 skill 名推算本地目录名（与后端 sanitize 规则一致的近似）。
function installedDirNameFromRemote(skill) {
  const match = installed.value.find(
    (item) => String(item.name || "").toLowerCase() === String(skill.name || "").toLowerCase(),
  );
  return match ? installedDirName(match) : String(skill.name || "");
}

function hasUpdate(skill) {
  return Boolean(updateStatuses.value[installedDirName(skill)]?.updateAvailable);
}

function updateCheckError(skill) {
  return updateStatuses.value[installedDirName(skill)]?.error || "";
}

const updatableInstalled = computed(() =>
  installed.value.filter((skill) => skill.sourceRepo && hasUpdate(skill)),
);

// 检查更新：仅支持由本面板安装（有来源记录）的 skill，手工放入的无从对照。
async function checkUpdates() {
  checkingUpdates.value = true;
  error.value = "";
  notice.value = "";
  try {
    const result = await checkSkillUpdates();
    const map = {};
    for (const status of Array.isArray(result) ? result : []) {
      if (status?.dirName) map[status.dirName] = status;
    }
    updateStatuses.value = map;
    const updatable = Object.values(map).filter((status) => status.updateAvailable).length;
    const failed = Object.values(map).filter((status) => status.error).length;
    if (updatable > 0) {
      notice.value = `发现 ${updatable} 个 skill 有更新`;
    } else if (failed > 0) {
      notice.value = `没有发现可用更新（${failed} 个检查失败）`;
    } else if (Object.keys(map).length === 0) {
      notice.value = "没有可检查的 skill：仅支持由本面板安装的 skill";
    } else {
      notice.value = "全部 skill 已是最新";
    }
  } catch (cause) {
    error.value = String(cause?.message || cause || "检查更新失败");
  } finally {
    checkingUpdates.value = false;
  }
}

// 全部更新：逐个按来源覆盖重装，失败不中断后续。
async function updateAll() {
  const targets = [...updatableInstalled.value];
  if (targets.length === 0) return;
  updatingAll.value = true;
  try {
    for (const skill of targets) {
      await updateInstalled(skill);
    }
  } finally {
    updatingAll.value = false;
  }
}

async function backupSkills() {
  backingUp.value = true;
  error.value = "";
  notice.value = "";
  try {
    const path = await backupSkillsToZip();
    if (path) {
      notice.value = `已备份到 ${path}`;
    }
  } catch (cause) {
    error.value = String(cause?.message || cause || "备份失败");
  } finally {
    backingUp.value = false;
  }
}

function requestRestore() {
  emit("confirm-action", {
    title: "恢复 Skills 备份",
    content: "将从备份 zip 恢复 skill，同名 skill 会被覆盖为备份中的版本，确定继续？",
    failureTitle: "恢复失败",
    run: async () => {
      try {
        const count = await restoreSkillsFromZip();
        if (count >= 0) {
          notice.value = `已恢复 ${count} 个 skill`;
          await refreshInstalled();
          markRemoteInstalled();
        }
        return { ok: true };
      } catch (cause) {
        return { ok: false, error: String(cause?.message || cause || "服务错误") };
      }
    },
  });
}

// 安装/卸载后同步远端卡片的"已安装"标记，避免重新走一次网络拉取。
function markRemoteInstalled() {
  const names = new Set(installed.value.map((skill) => installedDirName(skill).toLowerCase()));
  remote.value = remote.value.map((skill) => ({
    ...skill,
    installed: names.has(String(skill.name || "").toLowerCase()),
  }));
}

function installedDirName(skill) {
  return String(skill.path || "").split(/[\\/]/).pop() || String(skill.name || "");
}

function requestUninstall(skill) {
  const dirName = installedDirName(skill);
  emit("confirm-action", {
    title: "卸载 Skill",
    content: `将删除「${skill.name}」的本地目录，确定继续？`,
    failureTitle: "卸载失败",
    run: async () => {
      try {
        await uninstallSkill(dirName);
        await refreshInstalled();
        markRemoteInstalled();
        return { ok: true };
      } catch (cause) {
        return { ok: false, error: String(cause?.message || cause || "服务错误") };
      }
    },
  });
}

async function previewRemote(skill) {
  preview.value = { title: skill.name, content: "", loading: true };
  try {
    const content = await getRemoteSkillContent(fetchedRepo.value || selectedRepo.value, skill.subdir);
    if (preview.value) preview.value = { title: skill.name, content: String(content || ""), loading: false };
  } catch (cause) {
    if (preview.value) {
      preview.value = { title: skill.name, content: String(cause?.message || cause || "读取失败"), loading: false };
    }
  }
}

async function previewInstalled(skill) {
  preview.value = { title: skill.name, content: "", loading: true };
  try {
    const content = await getInstalledSkillContent(installedDirName(skill));
    if (preview.value) preview.value = { title: skill.name, content: String(content || ""), loading: false };
  } catch (cause) {
    if (preview.value) {
      preview.value = { title: skill.name, content: String(cause?.message || cause || "读取失败"), loading: false };
    }
  }
}

function closePreview() {
  preview.value = null;
}

function handleKeydown(event) {
  if (event.key !== "Escape") return;
  if (preview.value) {
    closePreview();
  } else if (addRepoDialog.value) {
    closeAddRepo();
  }
}

function isInstalling(skill) {
  return installingSubdirs.value.has(skill.subdir || "@root");
}

function isUpdating(skill) {
  return updatingNames.value.has(installedDirName(skill));
}

function formatSource(skill) {
  if (!skill.sourceRepo) return "";
  return skill.sourcePath ? `${skill.sourceRepo} / ${skill.sourcePath}` : skill.sourceRepo;
}

onMounted(() => {
  void refreshInstalled();
  void refreshRepos().then(() => {
    // 默认落在浏览页：仓库列表就绪后直接拉取当前仓库，打开面板即可挑选安装。
    if (selectedRepo.value && fetchedRepo.value !== selectedRepo.value) {
      void fetchRemote(false);
    }
  });
});
onActivated(() => {
  window.addEventListener("keydown", handleKeydown);
  void refreshInstalled();
});
onDeactivated(() => {
  window.removeEventListener("keydown", handleKeydown);
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", handleKeydown);
});
</script>

<template>
  <div class="relative flex h-full min-h-0 flex-col gap-3 overflow-hidden">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="inline-flex rounded-[8px] border border-[#343434] bg-[#202020] p-0.5">
        <button
          type="button"
          class="rounded-[6px] px-3 py-1.5 text-sm transition-colors duration-100"
          :class="activeTab === 'browse' ? 'bg-[#2f2f2f] text-white' : 'text-[#8f8f8f] hover:text-[#d4d4d4]'"
          @click="activeTab = 'browse'"
        >
          浏览安装
        </button>
        <button
          type="button"
          class="rounded-[6px] px-3 py-1.5 text-sm transition-colors duration-100"
          :class="activeTab === 'installed' ? 'bg-[#2f2f2f] text-white' : 'text-[#8f8f8f] hover:text-[#d4d4d4]'"
          @click="activeTab = 'installed'"
        >
          已安装 {{ installed.length }}
        </button>
      </div>
      <div class="center-row gap-2 text-xs text-[#737373]">
        <span class="hidden lg:inline">安装到 ~/.claude/skills，Cursor 下次对话自动生效</span>
        <Button variant="default" @click="openSkillsDirectory">打开目录</Button>
      </div>
    </div>

    <div v-if="error" class="rounded-[7px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]">
      {{ error }}
    </div>
    <div v-if="notice" class="rounded-[7px] border border-[#14532d] bg-[#102418] px-3 py-2 text-xs text-[#86efac]">
      {{ notice }}
    </div>

    <!-- 浏览安装 -->
    <template v-if="activeTab === 'browse'">
      <div class="flex flex-wrap items-center gap-2 rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2">
        <Select v-model="selectedRepo" class="min-w-[210px]" :options="repoOptions" aria-label="Skills 仓库" />
        <!-- loading 时 Button 自带 Spinner，图标要隐藏，否则出现两个圈。 -->
        <Button variant="default" :loading="remoteLoading" title="重新下载仓库" @click="fetchRemote(true)">
          <span v-if="!remoteLoading" class="icon-[mdi--refresh] text-[15px]"></span>
        </Button>
        <Button v-if="selectedRepoRemovable" variant="default" @click="removeSelectedRepo">移除仓库</Button>
        <input
          v-model="remoteFilter"
          class="skills-input w-[150px]"
          placeholder="过滤列表"
        />
        <span class="flex-1"></span>
        <Button variant="default" @click="openAddRepo">
          <span class="icon-[mdi--plus] text-[15px]"></span>
          <span>添加仓库</span>
        </Button>
      </div>

      <div class="min-h-0 flex-1 overflow-auto rounded-[8px] border border-[#343434] bg-[#171717]">
        <div v-if="remoteLoading" class="py-10 text-center text-sm text-[#737373]">正在下载仓库…</div>
        <div v-else-if="remote.length === 0" class="py-10 text-center text-sm text-[#737373]">
          该仓库中没有发现 SKILL.md
        </div>
        <div v-else-if="filteredRemote.length === 0" class="py-10 text-center text-sm text-[#737373]">
          没有匹配过滤条件的 skill
        </div>
        <div
          v-for="skill in filteredRemote"
          :key="skill.subdir || '@root'"
          class="flex cursor-pointer items-start justify-between gap-3 border-b border-[#262626] px-3 py-2 transition-colors duration-100 hover:bg-[#1f1f1f]"
          title="点击查看 SKILL.md"
          @click="previewRemote(skill)"
        >
          <div class="min-w-0">
            <div class="truncate text-sm text-[#e5e5e5]">
              {{ skill.name }}
              <span v-if="skill.installed && skill.updateAvailable" class="ml-1 rounded-sm bg-[#3a2c10] px-1 text-[10px] text-[#facc15]">有更新</span>
              <span v-else-if="skill.installed" class="ml-1 rounded-sm bg-[#123322] px-1 text-[10px] text-[#4ade80]">已安装</span>
            </div>
            <div class="mt-0.5 line-clamp-2 text-xs leading-relaxed text-[#8f8f8f]">{{ skill.description }}</div>
            <div v-if="skill.subdir" class="mt-0.5 truncate font-mono text-[10px] text-[#5f5f5f]">{{ skill.subdir }}</div>
          </div>
          <Button
            class="shrink-0"
            :variant="!skill.installed || skill.updateAvailable ? 'primary' : 'default'"
            :loading="isInstalling(skill)"
            @click.stop="install(skill)"
          >
            {{ skill.installed ? (skill.updateAvailable ? "更新" : "重新安装") : "安装" }}
          </Button>
        </div>
      </div>
    </template>

    <!-- 已安装 -->
    <template v-else>
      <div class="flex flex-wrap items-center gap-2 rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2">
        <input
          v-model="installedFilter"
          class="skills-input w-[160px]"
          placeholder="过滤已安装 skill"
        />
        <span class="flex-1"></span>
        <Button
          v-if="updatableInstalled.length > 0"
          variant="primary"
          :loading="updatingAll"
          @click="updateAll"
        >
          {{ `全部更新（${updatableInstalled.length}）` }}
        </Button>
        <Button
          variant="default"
          :loading="checkingUpdates"
          title="仅支持由本面板安装（有来源记录）的 skill；手工放入的无法检测"
          @click="checkUpdates"
        >
          检查更新
        </Button>
        <Button variant="default" :loading="backingUp" title="把全部 skill 打包为 zip" @click="backupSkills">备份</Button>
        <Button variant="default" title="从备份 zip 恢复，同名覆盖" @click="requestRestore">恢复</Button>
        <Button variant="default" :loading="installedLoading" title="重新扫描本地目录" @click="refreshInstalled">
          <span v-if="!installedLoading" class="icon-[mdi--refresh] text-[15px]"></span>
        </Button>
      </div>

      <div class="min-h-0 flex-1 overflow-auto rounded-[8px] border border-[#343434] bg-[#171717]">
        <div v-if="!installedLoading && installed.length === 0" class="py-10 text-center text-sm text-[#737373]">
          还没有安装任何 skill，去「浏览安装」页挑选
        </div>
        <div v-else-if="filteredInstalled.length === 0" class="py-10 text-center text-sm text-[#737373]">
          没有匹配过滤条件的 skill
        </div>
        <div
          v-for="skill in filteredInstalled"
          :key="skill.path"
          class="group flex cursor-pointer items-start justify-between gap-3 border-b border-[#262626] px-3 py-2 transition-colors duration-100 hover:bg-[#1f1f1f]"
          title="点击查看 SKILL.md"
          @click="previewInstalled(skill)"
        >
          <div class="min-w-0">
            <div class="truncate text-sm text-[#e5e5e5]">
              {{ skill.name }}
              <span v-if="hasUpdate(skill)" class="ml-1 rounded-sm bg-[#3a2c10] px-1 text-[10px] text-[#facc15]">有更新</span>
              <span
                v-else-if="updateCheckError(skill)"
                class="ml-1 rounded-sm bg-[#3a1c1c] px-1 text-[10px] text-[#f87171]"
                :title="updateCheckError(skill)"
              >检查失败</span>
            </div>
            <div class="mt-0.5 line-clamp-2 text-xs leading-relaxed text-[#8f8f8f]">{{ skill.description }}</div>
            <div v-if="formatSource(skill)" class="mt-0.5 truncate font-mono text-[10px] text-[#5f5f5f]">
              {{ formatSource(skill) }}
            </div>
          </div>
          <div class="center-row shrink-0 gap-1.5">
            <Button
              v-if="skill.sourceRepo"
              :variant="hasUpdate(skill) ? 'primary' : 'default'"
              :loading="isUpdating(skill)"
              title="按来源仓库重新安装到最新版本"
              @click.stop="updateInstalled(skill)"
            >
              更新
            </Button>
            <button
              type="button"
              class="rounded-[5px] border border-transparent px-1.5 py-1 text-xs text-[#737373] opacity-0 transition-opacity duration-100 hover:border-[#4b1d1d] hover:bg-[#2a1313] hover:text-[#fca5a5] group-hover:opacity-100"
              @click.stop="requestUninstall(skill)"
            >
              卸载
            </button>
          </div>
        </div>
      </div>
    </template>

    <!-- 添加仓库弹层 -->
    <div
      v-if="addRepoDialog"
      class="absolute inset-0 z-20 flex items-center justify-center bg-black/60 p-6"
      @click.self="closeAddRepo"
    >
      <div class="w-full max-w-[440px] rounded-[10px] border border-[#3a3a3a] bg-[#1c1c1c] p-4 shadow-xl">
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-[#e5e5e5]">添加 Skills 仓库</span>
          <button type="button" class="text-[#8f8f8f] hover:text-white" title="Esc" @click="closeAddRepo">
            <span class="icon-[mdi--close] text-[18px]"></span>
          </button>
        </div>
        <input
          ref="addRepoInput"
          v-model="newRepoSpec"
          class="skills-input mt-3 w-full"
          placeholder="owner/repo"
          @keyup.enter="addRepo"
        />
        <div class="mt-2 text-xs leading-relaxed text-[#737373]">
          支持格式：owner/repo、owner/repo@branch、GitHub 仓库链接
        </div>
        <div v-if="addRepoError" class="mt-2 text-xs text-[#fca5a5]">{{ addRepoError }}</div>
        <div class="mt-4 flex justify-end gap-2">
          <Button variant="default" @click="closeAddRepo">取消</Button>
          <Button variant="primary" :loading="addingRepo" @click="addRepo">添加</Button>
        </div>
      </div>
    </div>

    <!-- SKILL.md 预览层 -->
    <div v-if="preview" class="absolute inset-0 z-20 flex items-center justify-center bg-black/60 p-6" @click.self="closePreview">
      <div class="flex max-h-full w-full max-w-[720px] flex-col overflow-hidden rounded-[10px] border border-[#3a3a3a] bg-[#1c1c1c] shadow-xl">
        <div class="flex items-center justify-between border-b border-[#2e2e2e] px-4 py-2.5">
          <span class="truncate text-sm font-medium text-[#e5e5e5]">{{ preview.title }} · SKILL.md</span>
          <button type="button" class="text-[#8f8f8f] hover:text-white" title="Esc" @click="closePreview">
            <span class="icon-[mdi--close] text-[18px]"></span>
          </button>
        </div>
        <div class="min-h-0 flex-1 overflow-auto px-4 py-3">
          <div v-if="preview.loading" class="py-8 text-center text-sm text-[#737373]">正在读取…</div>
          <pre v-else class="whitespace-pre-wrap break-words font-mono text-xs leading-relaxed text-[#c9c9c9]">{{ preview.content }}</pre>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.skills-input {
  border: 1px solid #3a3a3a;
  border-radius: 7px;
  background: #202020;
  padding: 6px 10px;
  color: #e5e5e5;
  outline: none;
  font-size: 13px;
}

.skills-input:focus {
  border-color: #1ca35a;
}
</style>
