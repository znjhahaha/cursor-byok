package client

import (
	"errors"
	"strings"

	serverconfig "cursor/internal/backend/server/config"
)

// ModelAdapterModelsRequest 描述模型编辑器发起一次「获取模型」所需的最小信息。
//
// 编辑器里的模型可能绑定了中转站，也可能是独立配置：
//   - 绑定中转站时只传 ProviderID，连接信息以站点配置为准；
//   - 独立配置时传 BaseURL / APIKey / 自定义头，临时组装成一次性站点。
type ModelAdapterModelsRequest struct {
	Type                 string `json:"type"`
	ProviderID           string `json:"providerID"`
	BaseURL              string `json:"baseURL"`
	APIKey               string `json:"apiKey"`
	ClientProfile        string `json:"clientProfile"`
	CustomHeadersEnabled bool   `json:"customHeadersEnabled"`
	CustomHeadersJSON    string `json:"customHeadersJSON"`
}

// ModelAdapterModelsResult 是一次「获取模型」的结果，只保留下拉框需要的字段。
type ModelAdapterModelsResult struct {
	Models    []ProviderModel `json:"models"`
	FromCache bool            `json:"fromCache"`
}

// FetchModelAdapterModels 为模型编辑器提供模型下拉候选。
//
// 它不自建拉取链路，而是把编辑器表单折算成一个 ProviderConfig，复用
// ListProviderModels 的路径探测、鉴权指纹、磁盘缓存与事件广播，
// 避免同一职责在两处各写一套探测规则。
func (s *ProxyService) FetchModelAdapterModels(input ModelAdapterModelsRequest) (ModelAdapterModelsResult, error) {
	provider, err := s.resolveModelAdapterProvider(input)
	if err != nil {
		return ModelAdapterModelsResult{Models: []ProviderModel{}}, err
	}

	result, err := s.ListProviderModels(provider)
	if err != nil {
		return ModelAdapterModelsResult{Models: []ProviderModel{}}, err
	}
	if !result.OK {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = "拉取模型列表失败"
		}
		return ModelAdapterModelsResult{Models: []ProviderModel{}}, errors.New(message)
	}
	return ModelAdapterModelsResult{Models: result.Models, FromCache: result.FromCache}, nil
}

// resolveModelAdapterProvider 决定这次拉取到底用谁的连接信息。
//
// 绑定了中转站就直接用站点配置，这样编辑器与「批量导入」看到的是同一份缓存；
// 未绑定才用表单里的临时值，并造一个不落盘的匿名站点。
func (s *ProxyService) resolveModelAdapterProvider(input ModelAdapterModelsRequest) (serverconfig.ProviderConfig, error) {
	if providerID := strings.TrimSpace(input.ProviderID); providerID != "" {
		provider, found := s.findProviderByID(providerID)
		if !found {
			return serverconfig.ProviderConfig{}, errors.New("所属中转站不存在或已被删除，请重新选择")
		}
		return provider, nil
	}

	adapterType := strings.ToLower(strings.TrimSpace(input.Type))
	if adapterType != "openai" && adapterType != "anthropic" {
		return serverconfig.ProviderConfig{}, errors.New("模型类型仅支持 OpenAI 或 Anthropic")
	}
	if strings.TrimSpace(input.BaseURL) == "" {
		return serverconfig.ProviderConfig{}, errors.New("接口地址不能为空")
	}
	if strings.TrimSpace(input.APIKey) == "" {
		return serverconfig.ProviderConfig{}, errors.New("访问密钥不能为空")
	}

	headersJSON := ""
	if input.CustomHeadersEnabled {
		headersJSON = strings.TrimSpace(input.CustomHeadersJSON)
	}
	return serverconfig.ProviderConfig{
		Name:          "模型编辑器",
		Type:          adapterType,
		BaseURL:       strings.TrimSpace(input.BaseURL),
		APIKey:        strings.TrimSpace(input.APIKey),
		ClientProfile: strings.TrimSpace(input.ClientProfile),
		HeadersJSON:   headersJSON,
	}, nil
}

// findProviderByID 从当前已保存配置里取出中转站，找不到返回 false。
func (s *ProxyService) findProviderByID(providerID string) (serverconfig.ProviderConfig, bool) {
	if s == nil {
		return serverconfig.ProviderConfig{}, false
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return serverconfig.ProviderConfig{}, false
	}
	for _, provider := range cfg.Providers {
		if strings.TrimSpace(provider.ID) == providerID {
			return provider, true
		}
	}
	return serverconfig.ProviderConfig{}, false
}