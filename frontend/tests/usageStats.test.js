import { describe, expect, it } from "vitest";

import { stableUsageModelKey, usageCacheHitRate } from "@/utils/usageStats";

describe("用量统计稳定模型身份", () => {
  it("共享 wire model 的两个渠道仍使用不同筛选键", () => {
    const wireModel = "claude-opus-5";
    expect(stableUsageModelKey({ channelID: "baibei", model: wireModel }))
      .not.toBe(stableUsageModelKey({ channelID: "anyrouter", model: wireModel }));
  });

  it("无法归属的旧模型进入历史桶", () => {
    expect(stableUsageModelKey({ model: "claude-opus-5" })).toBe("legacy:claude-opus-5");
  });

  it("缓存命中率分母排除估算输入", () => {
    expect(usageCacheHitRate({
      cacheReadTokens: 80,
      inputTokens: 120,
      estimatedInputTokens: 40,
    })).toBeCloseTo(0.5);
  });
});
