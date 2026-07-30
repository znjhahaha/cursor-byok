package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cursor/internal/modelchannel"
)

// ProviderConfig 描述一个 API 中转站。
//
// 与 ModelAdapterConfig.ID 不同，这里的 ID 会落盘：adapter 通过 ProviderID 引用中转站，
// 引用关系必须在配置文件重启后依然成立，因此不能使用内容哈希。
type ProviderConfig struct {
	ID            string `json:"id" yaml:"id"`
	Name          string `json:"name" yaml:"name"`
	Type          string `json:"type" yaml:"type"`
	BaseURL       string `json:"baseURL" yaml:"baseURL"`
	APIKey        string `json:"apiKey" yaml:"apiKey"`
	ClientProfile string `json:"clientProfile,omitempty" yaml:"clientProfile,omitempty"`
	UserAgent     string `json:"userAgent" yaml:"userAgent"`
	HeadersJSON   string `json:"headersJSON" yaml:"headersJSON"`
	ModelsPath    string `json:"modelsPath" yaml:"modelsPath"`
	InferencePath string `json:"inferencePath" yaml:"inferencePath"`
	Note          string `json:"note" yaml:"note"`
	Builtin       bool   `json:"builtin" yaml:"builtin"`
	HomeURL       string `json:"homeURL" yaml:"homeURL"`
}

const providerIDHexLength = 12

// NormalizeProviderConfigs 校验并归一化中转站列表，同时补齐缺失的内置预设。
//
// 内置预设的合并策略是「以用户数据为准」：只补齐用户没有的站点，
// 已存在的条目只强制回填 Builtin 标记与官网地址，其余字段（尤其是 apiKey）保持用户所填。
func NormalizeProviderConfigs(input []ProviderConfig) ([]ProviderConfig, error) {
	normalized := make([]ProviderConfig, 0, len(input)+len(BuiltinProviders))
	seenIDs := make(map[string]struct{}, len(input))

	for _, item := range input {
		next, err := NormalizeProviderConfig(item)
		if err != nil {
			return nil, err
		}
		if _, exists := seenIDs[next.ID]; exists {
			return nil, fmt.Errorf("中转站 %s 的 id 重复", next.Name)
		}
		seenIDs[next.ID] = struct{}{}
		normalized = append(normalized, next)
	}

	for _, preset := range BuiltinProviders {
		index := indexOfProvider(normalized, preset.ID)
		if index < 0 {
			normalized = append(normalized, preset)
			seenIDs[preset.ID] = struct{}{}
			continue
		}
		normalized[index].Builtin = true
		if strings.TrimSpace(normalized[index].ClientProfile) == "" {
			normalized[index].ClientProfile = preset.ClientProfile
		}
		if isLegacyBuiltinUserAgent(normalized[index].UserAgent) {
			normalized[index].UserAgent = ""
		}
		if strings.TrimSpace(normalized[index].HomeURL) == "" {
			normalized[index].HomeURL = preset.HomeURL
		}
	}

	return normalized, nil
}

// NormalizeProviderConfig 校验并归一化单个中转站，缺失 id 时按内容派生。
func NormalizeProviderConfig(item ProviderConfig) (ProviderConfig, error) {
	next := ProviderConfig{
		ID:            strings.TrimSpace(item.ID),
		Name:          strings.TrimSpace(item.Name),
		Type:          normalizeModelAdapterType(item.Type),
		APIKey:        strings.TrimSpace(item.APIKey),
		ClientProfile: strings.ToLower(strings.TrimSpace(item.ClientProfile)),
		UserAgent:     strings.TrimSpace(item.UserAgent),
		HeadersJSON:   strings.TrimSpace(item.HeadersJSON),
		ModelsPath:    normalizeProviderPath(item.ModelsPath),
		InferencePath: normalizeProviderPath(item.InferencePath),
		Note:          strings.TrimSpace(item.Note),
		Builtin:       item.Builtin,
		HomeURL:       strings.TrimSpace(item.HomeURL),
	}
	if next.Name == "" {
		return ProviderConfig{}, errors.New("中转站名称不能为空")
	}
	if next.Type == "" {
		return ProviderConfig{}, fmt.Errorf("中转站 %s 的 type 仅支持 openai 或 anthropic", next.Name)
	}
	if next.ClientProfile != "" && !isSupportedClientProfile(next.ClientProfile) {
		return ProviderConfig{}, fmt.Errorf("中转站 %s 的 clientProfile 仅支持 generic、claude-code 或 codex", next.Name)
	}
	baseURL, err := modelchannel.NormalizeBaseURL(item.BaseURL)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("中转站 %s 的地址无效: %w", next.Name, err)
	}
	// 落盘只保留站点根地址。用户粘贴的完整端点（如 https://x/v1/messages）以及
	// 历史配置里被探测结果污染的地址，都在这里收敛一次，避免请求期二次拼接。
	next.BaseURL = trimProviderConnectionEndpoint(baseURL)
	if next.HeadersJSON != "" {
		if err := validateHeadersJSON(next.HeadersJSON); err != nil {
			return ProviderConfig{}, fmt.Errorf("中转站 %s 的自定义请求头无效: %w", next.Name, err)
		}
	}
	if next.ID == "" {
		next.ID = buildProviderID(next.BaseURL, next.Name)
	}
	return next, nil
}

// buildProviderID 为新建的中转站生成稳定标识。
// 只取 baseURL 与名称参与哈希，保证用户后续修改密钥或请求头时引用关系不会断裂。
func buildProviderID(baseURL string, name string) string {
	payload := strings.TrimSpace(strings.ToLower(baseURL)) + "\n" + strings.TrimSpace(name)
	sum := sha256.Sum256([]byte(payload))
	return "p-" + hex.EncodeToString(sum[:])[:providerIDHexLength]
}

func indexOfProvider(providers []ProviderConfig, id string) int {
	target := strings.TrimSpace(id)
	if target == "" {
		return -1
	}
	for index := range providers {
		if providers[index].ID == target {
			return index
		}
	}
	return -1
}

// FindProvider 按 id 查找中转站。
func FindProvider(providers []ProviderConfig, id string) (ProviderConfig, bool) {
	index := indexOfProvider(providers, id)
	if index < 0 {
		return ProviderConfig{}, false
	}
	return providers[index], true
}

func normalizeProviderPath(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return strings.TrimRight(text, "/")
	}
	if !strings.HasPrefix(text, "/") {
		text = "/" + text
	}
	return strings.TrimRight(text, "/")
}

func trimProviderConnectionEndpoint(base string) string {
	lower := strings.ToLower(base)
	for _, suffix := range []string{
		"/v1/chat/completions",
		"/v1/responses",
		"/v1/messages",
		"/v1/models",
		"/chat/completions",
		"/responses",
		"/messages",
		"/models",
	} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimRight(base[:len(base)-len(suffix)], "/")
		}
	}
	return base
}

// ApplyProviderInheritance 把中转站的连接信息物化到模型适配器上。
//
// 物化必须发生在计算 channelID 之前：channelID 由 baseURL 与 apiKey 派生，
// 若留到运行时再补齐，同一中转站下的模型会基于空地址生成相同标识而互相冲突。
//
// 中转站是连接信息的权威源，因此 baseURL 强制覆盖；apiKey 仅在中转站已填写时覆盖，
// 让「站点未配密钥、模型自带密钥」这种过渡状态仍可用。
func ApplyProviderInheritance(adapter ModelAdapterConfig, providers []ProviderConfig) ModelAdapterConfig {
	provider, ok := FindProvider(providers, adapter.ProviderID)
	if !ok {
		return adapter
	}
	resolved := adapter
	// clientProfile 是模型可显式覆盖的请求行为。中转站只为旧配置中真正缺失
	// 该字段的模型提供默认值；generic 同样是有效的显式选择，不能被站点覆盖。
	if strings.TrimSpace(resolved.ClientProfile) == "" && strings.TrimSpace(provider.ClientProfile) != "" {
		resolved.ClientProfile = provider.ClientProfile
	}
	// baseURL 只承载站点根地址。协议端点（/v1/messages、/v1/responses 等）属于
	// adapter 的协议职责，由各 adapter 在发请求时派生，中转站不参与拼接，
	// 否则 Anthropic 探测出的 /v1/messages 会被带进 OpenAI 模型的请求地址。
	resolved.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if strings.TrimSpace(provider.APIKey) != "" {
		resolved.APIKey = provider.APIKey
	}
	if strings.TrimSpace(resolved.Type) == "" {
		resolved.Type = provider.Type
	}
	merged := mergeProviderHeaders(provider, resolved)
	if merged != "" {
		resolved.CustomHeadersEnabled = true
		resolved.CustomHeadersJSON = merged
	}
	return resolved
}

func isLegacyBuiltinUserAgent(value string) bool {
	switch strings.TrimSpace(value) {
	case "claude-cli/1.0.60 (external, cli)", "claude-cli/1.0.25":
		return true
	default:
		return false
	}
}

// RepairProviderReferences fixes stale persisted provider IDs without discarding
// otherwise complete model adapter connection data.
func RepairProviderReferences(adapters []ModelAdapterConfig, providers []ProviderConfig) []ModelAdapterConfig {
	repaired := append([]ModelAdapterConfig(nil), adapters...)
	for index := range repaired {
		adapter := repaired[index]
		if strings.TrimSpace(adapter.ProviderID) == "" {
			continue
		}
		if _, ok := FindProvider(providers, adapter.ProviderID); ok {
			continue
		}
		matches := make([]ProviderConfig, 0, 1)
		for _, provider := range providers {
			if strings.TrimSpace(adapter.Type) != "" && strings.TrimSpace(provider.Type) != strings.TrimSpace(adapter.Type) {
				continue
			}
			// 一律按站点根地址比较：迁移期落盘的 adapter.BaseURL 可能带着
			// 旧版物化进去的端点后缀，剥离后才能与新语义下的 provider 对上。
			providerBase := trimProviderConnectionEndpoint(strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/"))
			adapterBase := trimProviderConnectionEndpoint(strings.TrimRight(strings.TrimSpace(adapter.BaseURL), "/"))
			if strings.EqualFold(providerBase, adapterBase) {
				matches = append(matches, provider)
			}
		}
		if len(matches) == 1 {
			repaired[index].ProviderID = matches[0].ID
			continue
		}
		if strings.TrimSpace(adapter.BaseURL) != "" && strings.TrimSpace(adapter.APIKey) != "" {
			repaired[index].ProviderID = ""
		}
	}
	return repaired
}

// ApplyProviderInheritanceAll 对整份适配器列表做物化。
func ApplyProviderInheritanceAll(adapters []ModelAdapterConfig, providers []ProviderConfig) []ModelAdapterConfig {
	if len(adapters) == 0 {
		return []ModelAdapterConfig{}
	}
	resolved := make([]ModelAdapterConfig, 0, len(adapters))
	for _, adapter := range adapters {
		resolved = append(resolved, ApplyProviderInheritance(adapter, providers))
	}
	return resolved
}

// ValidateProviderReferences 拒绝指向不存在中转站的适配器。
//
// 悬空引用会让适配器失去连接信息却无法定位原因，因此在归一化阶段直接报错，
// 由调用方（删除中转站时）负责级联清理。
func ValidateProviderReferences(adapters []ModelAdapterConfig, providers []ProviderConfig) error {
	for _, adapter := range adapters {
		id := strings.TrimSpace(adapter.ProviderID)
		if id == "" {
			continue
		}
		if _, ok := FindProvider(providers, id); !ok {
			return fmt.Errorf("模型 %s 绑定的中转站已不存在", strings.TrimSpace(adapter.DisplayName))
		}
	}
	return nil
}

// mergeProviderHeaders 合并中转站与适配器两级请求头，适配器优先。
// 中转站的 UserAgent 以 User-Agent 头的形式参与合并，用于绕过按客户端标识做白名单的站点。
func mergeProviderHeaders(provider ProviderConfig, adapter ModelAdapterConfig) string {
	headers := map[string]string{}
	if strings.TrimSpace(provider.HeadersJSON) != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(provider.HeadersJSON), &parsed); err == nil {
			for key, value := range parsed {
				headers[key] = value
			}
		}
	}
	if agent := strings.TrimSpace(provider.UserAgent); agent != "" {
		headers["User-Agent"] = agent
	}
	if adapter.CustomHeadersEnabled && strings.TrimSpace(adapter.CustomHeadersJSON) != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(adapter.CustomHeadersJSON), &parsed); err == nil {
			for key, value := range parsed {
				headers[key] = value
			}
		}
	}
	if len(headers) == 0 {
		return ""
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return ""
	}
	return string(encoded)
}
