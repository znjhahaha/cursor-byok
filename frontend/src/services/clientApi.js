import {
  GetModelAdapterTestResults,
  GetDiagnosticLogs,
  ExportDiagnosticBundle,
  GetProviderModelsCache,
  GetState,
  ListProviderModels,
  LoadUserConfig,
  RefreshProviderModels,
  SaveUserConfig,
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

export function getDiagnosticLogs(query) {
  return withApiLogging("GetDiagnosticLogs", query, () => GetDiagnosticLogs(query));
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

export function getUsageSeries(days) {
  return withApiLogging("GetUsageSeries", { days }, () => GetUsageSeries(days));
}

export function openProviderEditor(providerJSON) {
  return withApiLogging("OpenProviderEditorWindow", { providerJSON }, () =>
    OpenProviderEditorWindow(providerJSON),
  );
}

export function getProviderEditorContext() {
  return withApiLogging("GetProviderEditorContext", undefined, () => GetProviderEditorContext());
}
