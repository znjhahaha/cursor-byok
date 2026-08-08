import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(() => () => {}) },
}));

vi.mock("@/services/clientApi", () => {
  const stub = vi.fn();
  return {
    checkForUpdates: stub,
    cancelModelAdapterTest: vi.fn(),
    getAppVersion: stub,
    getDetailedLoggingState: vi.fn(),
    getHomeMetricsSummary: stub,
    getModelAdapterTestResults: stub,
    getProviderModelsCache: stub,
    getUsageSeries: stub,
    installReadyUpdate: stub,
    getProxyState: stub,
    listProviderModels: stub,
    openConfigWindow: stub,
    loadUserConfig: stub,
    openLogsDirectory: stub,
    openModelConfig: stub,
    openModelEditor: stub,
    openProviderEditor: stub,
    refreshProviderModels: stub,
    saveUserConfig: stub,
    setDetailedLoggingEnabled: vi.fn(),
    startProxyService: stub,
    stopProxyService: stub,
    testModelAdapter: stub,
  };
});

import {
  appState,
  cancelModelAdapterTest,
  createEmptyModelAdapter,
  formatImportSummary,
  formatModelAdapterTestSummary,
  normalizeModelAdapter,
  normalizeProvider,
  repairModelAdapterProviderReferences,
  refreshDetailedLoggingState,
  saveDetailedLogging,
  saveModelAdapterOrder,
  startModelAdapterTest,
  validateModelAdapters,
  validateProviderDetails,
} from "@/state/appState";
import * as clientApi from "@/services/clientApi";

function providerFixture() {
  return normalizeProvider({
    id: "provider-1",
    name: "AnyRouter",
    type: "anthropic",
    baseURL: "https://anyrouter.top/v1/messages",
    apiKey: "secret",
    clientProfile: "claude-code",
  });
}

function adapterFixture() {
  return {
    ...createEmptyModelAdapter(),
    providerID: "provider-1",
    displayName: "Claude",
    type: "anthropic",
    tooltipData: "AnyRouter",
    modelID: "claude-sonnet-4-5",
    clientProfile: "claude-code",
    contextWindowTokens: 200_000,
    anthropic1MContextEnabled: true,
  };
}

describe("中转站继承校验", () => {
  it("传入 providers 时允许模型继承连接信息", () => {
    const provider = providerFixture();
    expect(provider.baseURL).toBe("https://anyrouter.top");
    expect(validateModelAdapters([adapterFixture()], [provider])).toBe("");
  });

  it("加载时把旧端点地址迁移为 provider 根地址", () => {
    const provider = providerFixture();
    const validReference = {
      ...adapterFixture(),
      baseURL: "https://anyrouter.top/v1/messages",
    };
    const staleReference = {
      ...validReference,
      providerID: "provider-stale",
    };
    const repaired = repairModelAdapterProviderReferences(
      [validReference, staleReference],
      [provider],
    );
    expect(repaired[0].baseURL).toBe("https://anyrouter.top");
    expect(repaired[1]).toMatchObject({
      providerID: "provider-1",
      baseURL: "https://anyrouter.top",
    });
  });

  it("缺少 providers 上下文时能复现悬空引用错误", () => {
    expect(validateModelAdapters([adapterFixture()])).toContain("绑定的中转站已不存在");
  });
});

describe("模型测试部分成功状态", () => {
  it("收到正文但流未完整结束时显示可用警告而不是超时失败", () => {
    expect(formatModelAdapterTestSummary({
      status: "success",
      availability: "available",
      benchmarkComplete: false,
      warning: "已收到有效文本，但测速流未完整结束",
      firstTextTokenMS: 5200,
    })).toContain("测速未完整结束");
  });
});

describe("1M 上下文持久化语义", () => {
  it("normalize 保留用户填写的上下文窗口", () => {
    const adapter = normalizeModelAdapter(adapterFixture());
    expect(adapter.anthropic1MContextEnabled).toBe(true);
    expect(adapter.contextWindowTokens).toBe(200_000);
  });
});

describe("详细日志开关", () => {
  beforeEach(() => {
    vi.mocked(clientApi.getDetailedLoggingState).mockReset();
    vi.mocked(clientApi.setDetailedLoggingEnabled).mockReset();
    appState.log = false;
    appState.detailedLoggingEffective = false;
    appState.detailedLoggingStateKnown = false;
  });

  it("以后端确认的实际状态作为开关结果", async () => {
    vi.mocked(clientApi.setDetailedLoggingEnabled).mockResolvedValue({
      enabled: true,
      configured: true,
      fileEnabled: true,
      debugEnabled: true,
    });

    await expect(saveDetailedLogging(true)).resolves.toMatchObject({ ok: true });
    expect(appState).toMatchObject({
      log: true,
      detailedLoggingEffective: true,
      detailedLoggingStateKnown: true,
    });
  });

  it("后端未实际启用时返回错误并回滚界面状态", async () => {
    vi.mocked(clientApi.setDetailedLoggingEnabled).mockResolvedValue({
      enabled: false,
      configured: true,
      fileEnabled: false,
      debugEnabled: true,
    });

    const result = await saveDetailedLogging(true);
    expect(result.ok).toBe(false);
    expect(result.error).toContain("日志开关未能在后端生效");
    expect(appState.log).toBe(false);
    expect(appState.detailedLoggingEffective).toBe(false);
  });

  it("刷新时同步持久化状态和实际文件状态", async () => {
    vi.mocked(clientApi.getDetailedLoggingState).mockResolvedValue({
      enabled: true,
      configured: true,
      fileEnabled: true,
      debugEnabled: true,
    });

    await refreshDetailedLoggingState();
    expect(appState.log).toBe(true);
    expect(appState.detailedLoggingEffective).toBe(true);
    expect(appState.detailedLoggingStateKnown).toBe(true);
  });

  it("实时排队状态包含次数、已等待时间和倒计时", () => {
    expect(formatModelAdapterTestSummary({
      status: "running",
      warmupWaiting: true,
      warmupAttempt: 3,
      warmupElapsedMS: 5200,
      warmupNextRetryMS: 750,
    })).toBe("排队中（第 3 次） | 已等待 5.2 s | 下次 750 ms");
  });

  it("排队测试结果保留可取消字段，取消后同步 canceled 状态", async () => {
    vi.mocked(clientApi.testModelAdapter).mockResolvedValue({
      adapterID: "adapter-warmup",
      requestHash: "hash",
      status: "running",
      warmupWaiting: true,
      warmupAttempt: 2,
      warmupElapsedMS: 1000,
      warmupNextRetryMS: 500,
      warmupCancelable: true,
      testKind: "connectivity",
    });
    vi.mocked(clientApi.cancelModelAdapterTest).mockResolvedValue({
      adapterID: "adapter-warmup",
      requestHash: "hash",
      status: "canceled",
      summaryText: "排队检测已取消",
    });

    const running = await startModelAdapterTest({ ...adapterFixture(), id: "adapter-warmup" });
    expect(running).toMatchObject({ warmupCancelable: true, warmupNextRetryMS: 500, testKind: "connectivity" });
    const canceled = await cancelModelAdapterTest("adapter-warmup");
    expect(canceled).toMatchObject({ status: "canceled", summaryText: "排队检测已取消" });
    expect(appState.modelAdapterTestResults["adapter-warmup"].status).toBe("canceled");
  });
});

describe("中转站编辑反馈", () => {
  it("地址错误返回可聚焦字段和示例", () => {
    const validation = validateProviderDetails([{
      name: "Relay",
      type: "anthropic",
      baseURL: "relay.example.com",
      clientProfile: "claude-code",
    }]);
    expect(validation?.field).toBe("baseURL");
    expect(validation?.message).toContain("https://anyrouter.top");
  });

  it("导入结果区分新增与重复", () => {
    expect(formatImportSummary({ added: 2, skipped: 1 })).toBe("已导入 2 个模型，跳过 1 个重复");
  });
});

describe("模型拖拽排序", () => {
  it("提交的 ID 序列与当前配置不一致时拒绝写盘", async () => {
    const provider = providerFixture();
    const stored = normalizeModelAdapter({ ...adapterFixture(), id: "adapter-a" });
    vi.mocked(clientApi.loadUserConfig).mockResolvedValue({
      providers: [provider],
      modelAdapters: [stored, { ...stored, id: "adapter-b", modelID: "claude-opus-4-1" }],
    });
    vi.mocked(clientApi.saveUserConfig).mockClear();

    const missingOne = await saveModelAdapterOrder(["adapter-a"]);
    expect(missingOne.ok).toBe(false);
    expect(missingOne.error).toContain("请刷新后重试");

    const duplicated = await saveModelAdapterOrder(["adapter-a", "adapter-a"]);
    expect(duplicated.ok).toBe(false);

    const unknown = await saveModelAdapterOrder(["adapter-a", "adapter-zzz"]);
    expect(unknown.ok).toBe(false);
    // 该测试文件里 loadUserConfig 与 saveUserConfig 共用同一个 stub，
    // 只有写盘调用会带载荷，用参数个数区分读和写。
    const persistCalls = vi.mocked(clientApi.saveUserConfig).mock.calls.filter((args) => args.length > 0);
    expect(persistCalls).toHaveLength(0);
  });
});
