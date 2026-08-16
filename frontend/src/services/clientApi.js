import {
  CancelModelAdapterTest,
  GetModelAdapterTestResults,
  GetDiagnosticLogs,
  GetDetailedLoggingState,
  DisconnectCursorAccount,
  ExportDiagnosticBundle,
  FetchModelAdapterModels,
  GetCursorAccountStatus,
  GetProviderModelsCache,
  GetState,
  ListProviderModels,
  LoadUserConfig,
  RefreshProviderModels,
  SaveUserConfig,
  SetDetailedLoggingEnabled,
  StartCursorAccountLogin,
  StartProxy,
  StopProxy,
  TestModelAdapter,
} from "@bindings/cursor/internal/bridge/proxyservice.js";
import {
  GetAdRuntime,
  OpenExternalURL as OpenAdExternalURL,
} from "@bindings/cursor/internal/bridge/adservice.js";
import {
  GetHomeMetricsSummary,
  GetUsageSeries,
} from "@bindings/cursor/internal/bridge/metricsservice.js";
import {
  DeleteConversation,
  ExportConversationMarkdown,
  GetConversationTranscript,
  SearchConversations,
} from "@bindings/cursor/internal/bridge/conversationsservice.js";
import {
  AddSkillRepo,
  BackupSkillsToZip,
  CheckSkillUpdates,
  FetchRemoteSkills,
  GetInstalledSkillContent,
  GetRemoteSkillContent,
  InstallSkill,
  ListInstalledSkills,
  ListSkillRepos,
  OpenSkillsDirectory,
  RemoveSkillRepo,
  RestoreSkillsFromZip,
  UninstallSkill,
} from "@bindings/cursor/internal/bridge/skillsservice.js";
import {
  CheckForUpdates,
  GetAppVersion,
  GetFooterAuthorInfo,
  InstallReadyUpdate,
  GetModelEditorContext,
  GetProviderEditorContext,
  OpenConfigWindow,
  OpenFooterAuthorHome,
  OpenHistoryWindow,
  OpenModelConfigWindow,
  OpenModelEditorWindow,
  OpenProviderEditorWindow,
} from "@bindings/cursor/internal/bridge/windowservice.js";

const API_LOG_PREFIX = "[clientApi]";

function logSuccess(name, payload, result) {
  console.log(`${API_LOG_PREFIX} ${name} response`, {
    payload,
    result,
  });
}

function logError(name, payload, error) {
  console.error(`${API_LOG_PREFIX} ${name} error`, {
    payload,
    error,
  });
}

function withApiLogging(name, payload, runner) {
  return Promise.resolve()
    .then(() => runner())
    .then((result) => {
      logSuccess(name, payload, result);
      return result;
    })
    .catch((error) => {
      logError(name, payload, error);
      throw error;
    });
}

export function loadUserConfig() {
  return withApiLogging("LoadUserConfig", undefined, () => LoadUserConfig());
}

export function saveUserConfig(payload) {
  return withApiLogging("SaveUserConfig", payload, () => SaveUserConfig(payload));
}

export function getCursorAccountStatus() {
  return withApiLogging("GetCursorAccountStatus", undefined, () => GetCursorAccountStatus());
}

export function startCursorAccountLogin() {
  return withApiLogging("StartCursorAccountLogin", undefined, () => StartCursorAccountLogin());
}

export function disconnectCursorAccount() {
  return withApiLogging("DisconnectCursorAccount", undefined, () => DisconnectCursorAccount());
}

export function getProxyState() {
  return withApiLogging("GetState", undefined, () => GetState());
}

export function getHomeMetricsSummary() {
  return withApiLogging("GetHomeMetricsSummary", undefined, () => GetHomeMetricsSummary());
}

// ADS_ENABLED 是前端侧的广告总开关，与 Go 侧 ads.Enabled 对应。
// 关闭时不发起相关 IPC 调用，直接返回空运行时，广告位因缺少 available 而不渲染。
const ADS_ENABLED = false;

export function getAdRuntime() {
  if (!ADS_ENABLED) {
    return Promise.resolve({ available: false, enabled: false, slots: [] });
  }
  return GetAdRuntime();
}

export function openAdExternalURL(url) {
  if (!ADS_ENABLED) {
    return Promise.resolve();
  }
  return OpenAdExternalURL(url);
}

export function startProxyService() {
  return withApiLogging("StartProxy", undefined, () => StartProxy());
}

export function stopProxyService() {
  return withApiLogging("StopProxy", undefined, () => StopProxy());
}

export function openLogsDirectory() {
  return withApiLogging("OpenHistoryWindow", undefined, () => OpenHistoryWindow());
}

export function openConfigWindow() {
  return withApiLogging("OpenConfigWindow", undefined, () => OpenConfigWindow());
}

export function getAppVersion() {
  return withApiLogging("GetAppVersion", undefined, () => GetAppVersion());
}

export function getFooterAuthorInfo() {
  return withApiLogging("GetFooterAuthorInfo", undefined, () => GetFooterAuthorInfo());
}

// UPDATER_ENABLED 是前端侧的自动更新总开关，与 Go 侧 updater.Enabled 对应。
// 关闭时不发起相关 IPC 调用，页脚与更新弹窗一并隐藏。
const UPDATER_ENABLED = false;

export function checkForUpdates() {
  if (!UPDATER_ENABLED) {
    return Promise.resolve();
  }
  return withApiLogging("CheckForUpdates", undefined, () => CheckForUpdates());
}

export function installReadyUpdate() {
  if (!UPDATER_ENABLED) {
    return Promise.resolve();
  }
  return withApiLogging("InstallReadyUpdate", undefined, () => InstallReadyUpdate());
}

export function isUpdaterEnabled() {
  return UPDATER_ENABLED;
}

export function openFooterAuthorHome() {
  return withApiLogging("OpenFooterAuthorHome", undefined, () => OpenFooterAuthorHome());
}

export function openModelConfig() {
  return withApiLogging("OpenModelConfigWindow", undefined, () => OpenModelConfigWindow());
}

export function openModelEditor(index, adapterJSON) {
  return withApiLogging("OpenModelEditorWindow", { index, adapterJSON }, () =>
    OpenModelEditorWindow(index, adapterJSON),
  );
}

export function getModelEditorContext() {
  return withApiLogging("GetModelEditorContext", undefined, () => GetModelEditorContext());
}

// TestModelAdapter 返回的是可取消的 promise，批量测试依赖它的 cancel()。
// withApiLogging 内部的 Promise.resolve() 链会把它降级成普通 Promise，因此这里直接挂 then。
export function testModelAdapter(adapter) {
  return TestModelAdapter(adapter).then(
    (result) => {
      logSuccess("TestModelAdapter", adapter, result);
      return result;
    },
    (error) => {
      logError("TestModelAdapter", adapter, error);
      throw error;
    },
  );
}

export function getModelAdapterTestResults() {
  return withApiLogging("GetModelAdapterTestResults", undefined, () => GetModelAdapterTestResults());
}

export function cancelModelAdapterTest(adapterID) {
  return withApiLogging("CancelModelAdapterTest", { adapterID }, () => CancelModelAdapterTest(adapterID));
}

export function getDiagnosticLogs(query) {
  return withApiLogging("GetDiagnosticLogs", query, () => GetDiagnosticLogs(query));
}

export function getDetailedLoggingState() {
  return withApiLogging("GetDetailedLoggingState", undefined, () => GetDetailedLoggingState());
}

export function setDetailedLoggingEnabled(value) {
  return withApiLogging("SetDetailedLoggingEnabled", { value }, () => SetDetailedLoggingEnabled(value));
}

export function exportDiagnosticBundle() {
  return withApiLogging("ExportDiagnosticBundle", undefined, () => ExportDiagnosticBundle());
}

export function listProviderModels(provider) {
  return withApiLogging("ListProviderModels", provider, () => ListProviderModels(provider));
}

export function refreshProviderModels(provider) {
  return withApiLogging("RefreshProviderModels", provider, () => RefreshProviderModels(provider));
}

export function getProviderModelsCache() {
  return withApiLogging("GetProviderModelsCache", undefined, () => GetProviderModelsCache());
}

export function fetchModelAdapterModels(payload) {
  return withApiLogging("FetchModelAdapterModels", payload, () => FetchModelAdapterModels(payload));
}

export function getUsageSeries(days) {
  return withApiLogging("GetUsageSeries", { days }, () => GetUsageSeries(days));
}

export function searchConversations(options) {
  return withApiLogging("SearchConversations", options, () => SearchConversations(options));
}

export function getConversationTranscript(conversationID) {
  return withApiLogging("GetConversationTranscript", { conversationID }, () =>
    GetConversationTranscript(conversationID),
  );
}

export function exportConversationMarkdown(conversationID) {
  return withApiLogging("ExportConversationMarkdown", { conversationID }, () =>
    ExportConversationMarkdown(conversationID),
  );
}

export function deleteConversation(conversationID) {
  return withApiLogging("DeleteConversation", { conversationID }, () =>
    DeleteConversation(conversationID),
  );
}

export function listInstalledSkills() {
  return withApiLogging("ListInstalledSkills", undefined, () => ListInstalledSkills());
}

export function listSkillRepos() {
  return withApiLogging("ListSkillRepos", undefined, () => ListSkillRepos());
}

export function addSkillRepo(spec) {
  return withApiLogging("AddSkillRepo", { spec }, () => AddSkillRepo(spec));
}

export function removeSkillRepo(repoID) {
  return withApiLogging("RemoveSkillRepo", { repoID }, () => RemoveSkillRepo(repoID));
}

export function fetchRemoteSkills(repoID, refresh) {
  return withApiLogging("FetchRemoteSkills", { repoID, refresh }, () =>
    FetchRemoteSkills(repoID, refresh),
  );
}

export function installSkill(repoID, subdir) {
  return withApiLogging("InstallSkill", { repoID, subdir }, () => InstallSkill(repoID, subdir));
}

export function uninstallSkill(dirName) {
  return withApiLogging("UninstallSkill", { dirName }, () => UninstallSkill(dirName));
}

export function openSkillsDirectory() {
  return withApiLogging("OpenSkillsDirectory", undefined, () => OpenSkillsDirectory());
}

export function getRemoteSkillContent(repoID, subdir) {
  return withApiLogging("GetRemoteSkillContent", { repoID, subdir }, () =>
    GetRemoteSkillContent(repoID, subdir),
  );
}

export function getInstalledSkillContent(dirName) {
  return withApiLogging("GetInstalledSkillContent", { dirName }, () =>
    GetInstalledSkillContent(dirName),
  );
}

export function backupSkillsToZip() {
  return withApiLogging("BackupSkillsToZip", undefined, () => BackupSkillsToZip());
}

export function restoreSkillsFromZip() {
  return withApiLogging("RestoreSkillsFromZip", undefined, () => RestoreSkillsFromZip());
}

export function checkSkillUpdates() {
  return withApiLogging("CheckSkillUpdates", undefined, () => CheckSkillUpdates());
}

export function openProviderEditor(providerJSON) {
  return withApiLogging("OpenProviderEditorWindow", { providerJSON }, () =>
    OpenProviderEditorWindow(providerJSON),
  );
}

export function getProviderEditorContext() {
  return withApiLogging("GetProviderEditorContext", undefined, () => GetProviderEditorContext());
}
