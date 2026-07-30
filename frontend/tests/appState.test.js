import { describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(() => () => {}) },
}));

vi.mock("@/services/clientApi", () => {
  const stub = vi.fn();
  return {
    checkForUpdates: stub,
    getAppVersion: stub,
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
    startProxyService: stub,
    stopProxyService: stub,
    testModelAdapter: stub,
  };
});

import {
  createEmptyModelAdapter,
  formatImportSummary,
  normalizeModelAdapter,
  normalizeProvider,
  repairModelAdapterProviderReferences,
  validateModelAdapters,
  validateProviderDetails,
} from "@/state/appState";

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

describe("1M 上下文持久化语义", () => {
  it("normalize 保留用户填写的上下文窗口", () => {
    const adapter = normalizeModelAdapter(adapterFixture());
    expect(adapter.anthropic1MContextEnabled).toBe(true);
    expect(adapter.contextWindowTokens).toBe(200_000);
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