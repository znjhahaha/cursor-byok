// 模型协议推断与显示名模板。
//
// 这里是「请求形态」的唯一决策处：给定一个模型 ID，决定它该用 Anthropic 还是 OpenAI
// 协议、走哪个端点、模拟哪个客户端指纹。中转站只提供连接信息（地址、密钥、请求头），
// 协议归模型自己——同一个站点下 claude-* 和 gpt-* 本来就该走不同的 wire format。
//
// 全部是纯函数，不读写 appState，也不依赖 Vue：appState 反向导入这里，方向单一。

export const CLIENT_PROFILE_GENERIC = "generic";
export const CLIENT_PROFILE_CLAUDE_CODE = "claude-code";
export const CLIENT_PROFILE_CODEX = "codex";

export const OPENAI_ENDPOINT_RESPONSES = "/v1/responses";
export const OPENAI_ENDPOINT_CHAT_COMPLETIONS = "/v1/chat/completions";
export const OPENAI_ENDPOINT_CUSTOM = "/custom";

export const MODEL_TYPE_OPENAI = "openai";
export const MODEL_TYPE_ANTHROPIC = "anthropic";

// 三种可推断出的请求形态。运行期约束（internal/backend/agent/model/client_profile.go）：
// claude-code 只在 Anthropic 生效，codex 只在 OpenAI + /v1/responses 生效，
// 所以指纹必须和协议、端点绑在一起给出，单独选一个会被静默降级成 generic。
const PROTOCOL_ANTHROPIC = Object.freeze({
  type: MODEL_TYPE_ANTHROPIC,
  openAIEndpoint: "",
  clientProfile: CLIENT_PROFILE_CLAUDE_CODE,
});

const PROTOCOL_OPENAI_RESPONSES = Object.freeze({
  type: MODEL_TYPE_OPENAI,
  openAIEndpoint: OPENAI_ENDPOINT_RESPONSES,
  clientProfile: CLIENT_PROFILE_CODEX,
});

const PROTOCOL_OPENAI_CHAT = Object.freeze({
  type: MODEL_TYPE_OPENAI,
  openAIEndpoint: OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  clientProfile: CLIENT_PROFILE_GENERIC,
});

// 顺序敏感：先命中者胜出。gpt-5 / codex 必须排在通用 gpt 规则之前，
// 否则会被后者吃掉而降级到 chat/completions。
const FAMILY_RULES = [
  { test: /(claude|sonnet|opus|haiku)/, protocol: PROTOCOL_ANTHROPIC },
  { test: /(gpt-?5|codex|^o[34]([-._]|$)|gpt-?oss)/, protocol: PROTOCOL_OPENAI_RESPONSES },
  {
    test: /(gpt|^o1([-._]|$)|gemini|deepseek|qwen|grok|glm|kimi|llama|mistral|doubao|ernie|hunyuan|minimax|step-|yi-)/,
    protocol: PROTOCOL_OPENAI_CHAT,
  },
];

export const PROTOCOL_OVERRIDE_AUTO = "auto";
export const PROTOCOL_OVERRIDE_ANTHROPIC = "anthropic";
export const PROTOCOL_OVERRIDE_OPENAI_RESPONSES = "openai-responses";
export const PROTOCOL_OVERRIDE_OPENAI_CHAT = "openai-chat";

const OVERRIDE_PROTOCOLS = {
  [PROTOCOL_OVERRIDE_ANTHROPIC]: PROTOCOL_ANTHROPIC,
  [PROTOCOL_OVERRIDE_OPENAI_RESPONSES]: PROTOCOL_OPENAI_RESPONSES,
  [PROTOCOL_OVERRIDE_OPENAI_CHAT]: PROTOCOL_OPENAI_CHAT,
};

export const DISPLAY_NAME_TEMPLATE_DEFAULT = "{provider}-{model}";

function asText(value) {
  return typeof value === "string" ? value.trim() : "";
}

// matchKey 只保留用于家族判定的部分：厂商前缀（openai/gpt-4o）、路由后缀（[1m]、:free）
// 都不改变模型家族，剥掉后规则表才不用为每种写法各开一条。
function buildMatchKey(modelID) {
  const text = asText(modelID).toLowerCase();
  if (!text) {
    return "";
  }
  const lastSegment = text.split("/").pop() ?? text;
  return lastSegment
    .replace(/\[[^\]]*\]/g, "")
    .replace(/[:@].*$/, "")
    .trim();
}

// providerFallbackProtocol 保持与「按站点协议照抄」完全一致的旧行为，
// 让规则表未覆盖的冷门模型不会因为这次改动而换协议。
function providerFallbackProtocol(provider) {
  const providerType = asText(provider?.type).toLowerCase() === MODEL_TYPE_ANTHROPIC
    ? MODEL_TYPE_ANTHROPIC
    : MODEL_TYPE_OPENAI;
  return {
    type: providerType,
    openAIEndpoint: providerType === MODEL_TYPE_OPENAI ? OPENAI_ENDPOINT_CHAT_COMPLETIONS : "",
    clientProfile: asText(provider?.clientProfile).toLowerCase() || CLIENT_PROFILE_GENERIC,
  };
}

// inferModelProtocol 按模型 ID 推断请求形态。
// source 供界面区分「推断出来的」与「回落站点默认的」，不参与请求。
export function inferModelProtocol(modelID, provider = null) {
  const matchKey = buildMatchKey(modelID);
  if (matchKey) {
    for (const rule of FAMILY_RULES) {
      if (rule.test.test(matchKey)) {
        return { ...rule.protocol, source: "inferred" };
      }
    }
  }
  return { ...providerFallbackProtocol(provider), source: "provider" };
}

export function isProtocolOverride(value) {
  return Boolean(OVERRIDE_PROTOCOLS[asText(value)]);
}

// resolveModelProtocol 是导入路径的唯一入口：显式覆盖优先，其次才推断。
export function resolveModelProtocol(modelID, provider = null, override = PROTOCOL_OVERRIDE_AUTO) {
  const overridden = OVERRIDE_PROTOCOLS[asText(override)];
  if (overridden) {
    return { ...overridden, source: "override" };
  }
  return inferModelProtocol(modelID, provider);
}

// buildAdapterDisplayName 用模板拼显示名，默认「站点名-模型ID」。
// 占位符解析为空时要顺带清掉相邻分隔符，否则会留下 "-gpt-5" 这种前导横线。
export function buildAdapterDisplayName({ providerName = "", modelID = "", template = "" } = {}) {
  const model = asText(modelID);
  const provider = asText(providerName);
  const pattern = asText(template) || DISPLAY_NAME_TEMPLATE_DEFAULT;
  const rendered = pattern
    .replaceAll("{provider}", provider)
    .replaceAll("{model}", model)
    .replace(/\s{2,}/g, " ")
    .replace(/^[\s\-_/·]+|[\s\-_/·]+$/g, "")
    .trim();
  return rendered || model || provider;
}