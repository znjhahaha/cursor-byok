export function stableUsageModelKey(item) {
  const source = item && typeof item === "object" ? item : {};
  const explicit = String(source.modelKey || source.channelID || "").trim();
  if (explicit) return explicit;
  return `legacy:${String(source.model || "").trim()}`;
}

export function usageCacheHitRate(item) {
  const source = item && typeof item === "object" ? item : {};
  const cacheRead = finiteNumber(source.cacheReadTokens);
  const exactInput = Math.max(
    0,
    finiteNumber(source.inputTokens) - finiteNumber(source.estimatedInputTokens),
  );
  const denominator = cacheRead + exactInput;
  return denominator > 0 ? cacheRead / denominator : null;
}

function finiteNumber(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}
