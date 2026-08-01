import { describe, expect, it } from "vitest";

import {
  buildAdapterDisplayName,
  CLIENT_PROFILE_CLAUDE_CODE,
  CLIENT_PROFILE_CODEX,
  CLIENT_PROFILE_GENERIC,
  inferModelProtocol,
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_RESPONSES,
  PROTOCOL_OVERRIDE_ANTHROPIC,
  PROTOCOL_OVERRIDE_AUTO,
  PROTOCOL_OVERRIDE_OPENAI_CHAT,
  resolveModelProtocol,
} from "@/state/modelFamily";

const openaiProvider = { type: "openai", clientProfile: CLIENT_PROFILE_CODEX };
const anthropicProvider = { type: "anthropic", clientProfile: CLIENT_PROFILE_CLAUDE_CODE };

describe("inferModelProtocol", () => {
  it("把 claude 家族判成 Anthropic 协议，即使站点是 OpenAI 类型", () => {
    const protocol = inferModelProtocol("claude-sonnet-4-5", openaiProvider);
    expect(protocol.type).toBe("anthropic");
    expect(protocol.openAIEndpoint).toBe("");
    expect(protocol.clientProfile).toBe(CLIENT_PROFILE_CLAUDE_CODE);
    expect(protocol.source).toBe("inferred");
  });

  it("把 gpt-5 / codex / o3 判成 Responses 端点并配 Codex 指纹", () => {
    for (const modelID of ["gpt-5.6-sol", "gpt5-codex", "o3-pro"]) {
      const protocol = inferModelProtocol(modelID, anthropicProvider);
      expect(protocol.type).toBe("openai");
      expect(protocol.openAIEndpoint).toBe(OPENAI_ENDPOINT_RESPONSES);
      expect(protocol.clientProfile).toBe(CLIENT_PROFILE_CODEX);
    }
  });

  it("其余通用模型走 chat/completions + 通用指纹", () => {
    for (const modelID of ["gpt-4o-mini", "gemini-2.5-pro", "deepseek-v3", "qwen3-max"]) {
      const protocol = inferModelProtocol(modelID, anthropicProvider);
      expect(protocol.type).toBe("openai");
      expect(protocol.openAIEndpoint).toBe(OPENAI_ENDPOINT_CHAT_COMPLETIONS);
      expect(protocol.clientProfile).toBe(CLIENT_PROFILE_GENERIC);
    }
  });

  it("忽略厂商前缀与路由后缀", () => {
    expect(inferModelProtocol("anthropic/claude-3-7-sonnet[1m]", openaiProvider).type).toBe("anthropic");
    expect(inferModelProtocol("openai/gpt-5-mini:free", anthropicProvider).openAIEndpoint).toBe(
      OPENAI_ENDPOINT_RESPONSES,
    );
  });

  it("规则表未覆盖时回落到站点默认，行为与改动前一致", () => {
    const fallback = inferModelProtocol("some-private-model", anthropicProvider);
    expect(fallback.type).toBe("anthropic");
    expect(fallback.clientProfile).toBe(CLIENT_PROFILE_CLAUDE_CODE);
    expect(fallback.source).toBe("provider");

    const openaiFallback = inferModelProtocol("", openaiProvider);
    expect(openaiFallback.type).toBe("openai");
    expect(openaiFallback.openAIEndpoint).toBe(OPENAI_ENDPOINT_CHAT_COMPLETIONS);
  });
});

describe("resolveModelProtocol", () => {
  it("显式覆盖优先于推断", () => {
    expect(resolveModelProtocol("claude-sonnet-4-5", openaiProvider, PROTOCOL_OVERRIDE_OPENAI_CHAT)).toMatchObject({
      type: "openai",
      openAIEndpoint: OPENAI_ENDPOINT_CHAT_COMPLETIONS,
      clientProfile: CLIENT_PROFILE_GENERIC,
      source: "override",
    });
    expect(resolveModelProtocol("gpt-5", openaiProvider, PROTOCOL_OVERRIDE_ANTHROPIC).type).toBe("anthropic");
  });

  it("auto 与未知覆盖值都退回推断", () => {
    expect(resolveModelProtocol("gpt-5", openaiProvider, PROTOCOL_OVERRIDE_AUTO).source).toBe("inferred");
    expect(resolveModelProtocol("gpt-5", openaiProvider, "不存在的模式").source).toBe("inferred");
  });
});

describe("buildAdapterDisplayName", () => {
  it("默认拼成「站点名-模型ID」", () => {
    expect(buildAdapterDisplayName({ providerName: "炸弹", modelID: "gpt-5.6-sol" })).toBe("炸弹-gpt-5.6-sol");
  });

  it("支持自定义模板与占位符换序", () => {
    expect(
      buildAdapterDisplayName({ providerName: "炸弹", modelID: "gpt-5", template: "{model} @ {provider}" }),
    ).toBe("gpt-5 @ 炸弹");
  });

  it("占位符为空时清掉多余分隔符", () => {
    expect(buildAdapterDisplayName({ providerName: "", modelID: "gpt-5" })).toBe("gpt-5");
    expect(buildAdapterDisplayName({ providerName: "炸弹", modelID: "" })).toBe("炸弹");
  });

  it("模板不含任何占位符时至少回落到模型 ID", () => {
    expect(buildAdapterDisplayName({ providerName: "炸弹", modelID: "gpt-5", template: "---" })).toBe("gpt-5");
  });
});