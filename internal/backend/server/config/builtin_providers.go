package config

// BuiltinProviders 是随应用内置的中转站预设。
//
// 这里只声明连接方式，不含密钥；用户填入自己的密钥后，归一化过程会保留用户数据，
// 后续版本更新预设也不会覆盖已填字段。
var BuiltinProviders = []ProviderConfig{
	{
		ID:            "builtin-anyrouter",
		Name:          "AnyRouter",
		Type:          "anthropic",
		BaseURL:       "https://anyrouter.top",
		ClientProfile: "claude-code",
		HomeURL:       "https://anyrouter.top",
		Note:          "Claude Code 中转，兼容 Anthropic 协议",
		WarmupEnabled: true,
		Builtin:       true,
	},
	{
		// 实测该站点对 User-Agent 做白名单：非白名单客户端会在进入鉴权前
		// 就被 unauthorized_client_error 拦截，因此必须伪装成 Claude Code CLI。
		ID:            "builtin-agentrouter",
		Name:          "AgentRouter",
		Type:          "anthropic",
		BaseURL:       "https://agentrouter.org",
		HomeURL:       "https://agentrouter.org",
		ClientProfile: "claude-code",
		Note:          "需要 Claude Code 客户端标识才能通过校验",
		Builtin:       true,
	},
	{
		ID:            "builtin-znj",
		Name:          "ZNJ API",
		Type:          "openai",
		BaseURL:       "https://znj1234-znj-api.hf.space",
		ClientProfile: "codex",
		HomeURL:       "https://znj1234-znj-api.hf.space",
		Note:          "OpenAI 协议兼容站点",
		Builtin:       true,
	},
}
