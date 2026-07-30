package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/netproxy"
)

const (
	providerModelsTimeout       = 8 * time.Second
	providerModelsMaxBodyBytes  = 1 << 20
	providerModelsAnthropicVers = "2023-06-01"
)

var providerModelsPathCandidates = []string{
	"/v1/models",
	"/models",
	"/api/v1/models",
}

var providerOpenAIInferencePathCandidates = []string{
	"/v1/chat/completions",
	"/chat/completions",
	"/v1/responses",
	"/responses",
}

var providerAnthropicInferencePathCandidates = []string{
	"/v1/messages",
	"/messages",
}

// ProviderModel 描述中转站返回的一个可用模型。
type ProviderModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnedBy string `json:"ownedBy"`
}

// ProviderModelsResult 是一次模型列表拉取的结果。
//
// 失败不通过 error 单独返回，而是与请求 URL 一并放进结果里：
// 中转站的排障几乎总是需要「打到了哪个地址 + 站点原话」这两条信息。
type ProviderModelsResult struct {
	ProviderID    string          `json:"providerID"`
	RequestURL    string          `json:"requestURL"`
	ResolvedPath  string          `json:"resolvedPath"`
	InferencePath string          `json:"inferencePath"`
	Reachable     bool            `json:"reachable"`
	OK            bool            `json:"ok"`
	Error         string          `json:"error"`
	Attempts      []string        `json:"attempts"`
	Models        []ProviderModel `json:"models"`
	// RequestHash 是本次请求的内容指纹，供前端判断缓存是否已过期。
	RequestHash string `json:"requestHash"`
	// FetchedAt 是结果产生时间（unix 毫秒），供 UI 显示「上次更新」。
	FetchedAt int64 `json:"fetchedAt"`
	// FromCache 表示本次结果直接来自缓存，没有发起网络请求。
	FromCache bool `json:"fromCache"`
}

// ListProviderModels 拉取中转站的模型列表，供批量导入与探活使用。
//
// 缓存优先：探测一个陌生站点最坏要串行发 7 个各带 8s 超时的请求，
// 而中转站的模型列表几乎不变，所以命中缓存就直接返回，不重复付这个代价。
func (s *ProxyService) ListProviderModels(provider serverconfig.ProviderConfig) (ProviderModelsResult, error) {
	return s.resolveProviderModels(provider, false)
}

// RefreshProviderModels 跳过缓存强制重新拉取，供 UI 上的刷新入口使用。
func (s *ProxyService) RefreshProviderModels(provider serverconfig.ProviderConfig) (ProviderModelsResult, error) {
	return s.resolveProviderModels(provider, true)
}

func (s *ProxyService) resolveProviderModels(provider serverconfig.ProviderConfig, force bool) (ProviderModelsResult, error) {
	normalized, err := serverconfig.NormalizeProviderConfig(provider)
	if err != nil {
		return ProviderModelsResult{
			ProviderID: strings.TrimSpace(provider.ID),
			OK:         false,
			Error:      err.Error(),
			Attempts:   []string{},
			Models:     []ProviderModel{},
		}, err
	}

	requestHash := buildProviderModelsRequestHash(normalized)
	if !force && s != nil && s.providerModels != nil {
		if cached, ok := s.providerModels.Get(requestHash); ok {
			cached.FromCache = true
			return cached, nil
		}
	}

	result := fetchProviderModelsResult(normalized)
	result.RequestHash = requestHash
	result.FetchedAt = time.Now().UnixMilli()
	result.FromCache = false
	s.storeAndEmitProviderModels(normalized, result)
	return result, nil
}

// effectiveProviderAfterProbe 返回把探测结果回填之后的 provider。
//
// 调用方拿到结果后会把 ResolvedPath / InferencePath 物化进配置，而这两个字段参与
// 请求哈希，所以下一次带着物化后的配置进来时哈希必然不同。缓存必须同时按物化后的
// 哈希存一份，否则第二次调用永远命中不了。
func effectiveProviderAfterProbe(provider serverconfig.ProviderConfig, result ProviderModelsResult) serverconfig.ProviderConfig {
	effective := provider
	if resolved := strings.TrimSpace(result.ResolvedPath); resolved != "" {
		effective.ModelsPath = resolved
	}
	if inference := strings.TrimSpace(result.InferencePath); inference != "" {
		effective.InferencePath = inference
	}
	return effective
}

// fetchProviderModelsResult 执行真实的探测与拉取，不涉及缓存。
func fetchProviderModelsResult(normalized serverconfig.ProviderConfig) ProviderModelsResult {
	result := ProviderModelsResult{
		ProviderID: normalized.ID,
		Attempts:   []string{},
		Models:     []ProviderModel{},
	}

	for _, candidate := range buildProviderModelsCandidates(normalized) {
		result.RequestURL = candidate.URL
		models, fetchErr := fetchProviderModels(candidate.URL, normalized)
		if fetchErr != nil {
			result.Attempts = append(result.Attempts, candidate.Path+": "+fetchErr.Error())
			continue
		}
		result.OK = true
		result.Reachable = true
		result.ResolvedPath = candidate.Path
		result.Models = models
		if configured := strings.TrimSpace(normalized.InferencePath); configured != "" {
			if isAbsoluteHTTPURL(configured) {
				result.InferencePath = configured
			} else {
				result.InferencePath = ensureLeadingSlash(configured)
			}
		} else {
			inferencePath, inferenceAttempts, _ := probeProviderInferencePath(normalized)
			result.InferencePath = inferencePath
			result.Attempts = append(result.Attempts, inferenceAttempts...)
		}
		return result
	}

	inferencePath, inferenceAttempts, reachable := probeProviderInferencePath(normalized)
	result.InferencePath = inferencePath
	result.Reachable = reachable
	result.Attempts = append(result.Attempts, inferenceAttempts...)
	if reachable {
		result.Error = "站点可达，但没有可识别的模型列表；可手动添加模型 ID"
		return result
	}
	result.Error = "无法连接中转站，请检查地址、密钥与 API 路径"
	return result
}

type providerURLCandidate struct {
	Path string
	URL  string
}

func buildProviderModelsCandidates(provider serverconfig.ProviderConfig) []providerURLCandidate {
	rawBase := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	path := strings.TrimSpace(provider.ModelsPath)
	if path != "" {
		if isAbsoluteHTTPURL(path) {
			return []providerURLCandidate{{Path: path, URL: path}}
		}
		path = ensureLeadingSlash(path)
		return []providerURLCandidate{{Path: path, URL: joinProviderPath(rawBase, path)}}
	}
	base := trimProviderAPIPathSuffix(rawBase)
	candidates := make([]providerURLCandidate, 0, len(providerModelsPathCandidates))
	for _, candidate := range providerModelsPathCandidates {
		candidates = append(candidates, providerURLCandidate{Path: candidate, URL: base + candidate})
	}
	return candidates
}

// buildProviderModelsURL 保留为单地址辅助函数，供既有调用与测试使用。
func buildProviderModelsURL(provider serverconfig.ProviderConfig) string {
	candidates := buildProviderModelsCandidates(provider)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].URL
}

func isAbsoluteHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func ensureLeadingSlash(value string) string {
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

func joinProviderPath(base string, path string) string {
	normalizedBase := strings.TrimRight(strings.TrimSpace(base), "/")
	normalizedPath := ensureLeadingSlash(strings.TrimSpace(path))
	lowerBase := strings.ToLower(normalizedBase)
	lowerPath := strings.ToLower(normalizedPath)
	if strings.HasSuffix(lowerBase, lowerPath) {
		return normalizedBase
	}
	if strings.HasSuffix(lowerBase, "/v1") && strings.HasPrefix(lowerPath, "/v1/") {
		return normalizedBase + normalizedPath[len("/v1"):]
	}
	return normalizedBase + normalizedPath
}

// providerAPIPathSuffixes 是需要从 baseURL 末尾剥离的已知 API 路径片段，长路径在前。
var providerAPIPathSuffixes = []string{
	"/v1/chat/completions",
	"/v1/responses",
	"/v1/messages",
	"/v1/models",
	"/chat/completions",
	"/responses",
	"/messages",
	"/models",
	"/v1",
}

func trimProviderAPIPathSuffix(base string) string {
	lower := strings.ToLower(base)
	for _, suffix := range providerAPIPathSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimRight(base[:len(base)-len(suffix)], "/")
		}
	}
	return base
}

func probeProviderInferencePath(provider serverconfig.ProviderConfig) (string, []string, bool) {
	rawBase := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	base := trimProviderAPIPathSuffix(rawBase)
	paths := providerOpenAIInferencePathCandidates
	if provider.Type == "anthropic" {
		paths = providerAnthropicInferencePathCandidates
	}
	configuredPath := strings.TrimSpace(provider.InferencePath)
	if configuredPath != "" {
		if isAbsoluteHTTPURL(configuredPath) {
			paths = []string{configuredPath}
		} else {
			paths = []string{ensureLeadingSlash(configuredPath)}
		}
	}

	attempts := make([]string, 0, len(paths))
	authCandidate := ""
	for _, path := range paths {
		requestURL := base + path
		if configuredPath != "" && !isAbsoluteHTTPURL(path) {
			requestURL = joinProviderPath(rawBase, path)
		}
		if isAbsoluteHTTPURL(path) {
			requestURL = path
		}
		status, err := probeProviderInferenceURL(requestURL, provider)
		if err != nil {
			attempts = append(attempts, path+": "+err.Error())
			continue
		}
		attempts = append(attempts, fmt.Sprintf("%s: HTTP %d", path, status))
		// 401/403 可能来自部署在路由之前的统一鉴权层，不能证明当前路径真实存在。
		// 先记为低置信候选，继续寻找能返回格式校验错误的确切端点。
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if authCandidate == "" {
				authCandidate = path
			}
			continue
		}
		// 404/405 表示路径不存在；其他状态均说明请求已经命中推理处理链。
		// 请求体故意使用不完整 JSON，因此 400/422 只做路由识别，不可能触发计费推理。
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			return path, attempts, true
		}
	}
	if authCandidate != "" {
		return authCandidate, attempts, true
	}
	return "", attempts, false
}

func probeProviderInferenceURL(requestURL string, provider serverconfig.ProviderConfig) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), providerModelsTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader([]byte(`{`)))
	if err != nil {
		return 0, fmt.Errorf("构造推理探测请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := applyProviderModelsHeaders(httpReq, provider); err != nil {
		return 0, err
	}
	resp, err := netproxy.NewHTTPClient(providerModelsTimeout).Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("请求推理端点失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

func fetchProviderModels(requestURL string, provider serverconfig.ProviderConfig) ([]ProviderModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), providerModelsTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造模型列表请求失败: %w", err)
	}
	if err := applyProviderModelsHeaders(httpReq, provider); err != nil {
		return nil, err
	}

	resp, err := netproxy.NewHTTPClient(providerModelsTimeout).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求模型列表失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildModelAdapterHTTPStatusError("模型列表", resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, providerModelsMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("读取模型列表响应失败: %w", err)
	}
	if bodyErr := buildModelAdapterProviderBodyError("模型列表", body); bodyErr != nil {
		return nil, bodyErr
	}
	return parseProviderModelsBody(body)
}

// applyProviderModelsHeaders 按协议装配认证头，再依次叠加站点 UA 与自定义头。
// 覆盖顺序固定为「协议默认 → UserAgent → HeadersJSON」，让用户配置始终有最终决定权。
func applyProviderModelsHeaders(httpReq *http.Request, provider serverconfig.ProviderConfig) error {
	httpReq.Header.Set("Accept", "application/json")
	apiKey := strings.TrimSpace(provider.APIKey)
	if provider.Type == "anthropic" {
		modeladapter.ApplyAnthropicCompatibleAuthHeaders(httpReq, apiKey)
		httpReq.Header.Set("anthropic-version", providerModelsAnthropicVers)
	} else if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(apiKey, "Bearer "))
	}
	modeladapter.ApplyClientProfileHeaders(httpReq, provider.ClientProfile)
	if agent := strings.TrimSpace(provider.UserAgent); agent != "" {
		httpReq.Header.Set("User-Agent", agent)
	}
	if headers := strings.TrimSpace(provider.HeadersJSON); headers != "" {
		if err := modeladapter.ApplyCustomHeaders(httpReq, true, headers); err != nil {
			return fmt.Errorf("应用自定义请求头失败: %w", err)
		}
	}
	return nil
}

// providerModelsPayload 覆盖两种常见响应形态：
// OpenAI 风格的 {"data":[...]} 与部分自建站点直接返回的 {"models":[...]}。
type providerModelsPayload struct {
	Data   []providerModelEntry `json:"data"`
	Models []providerModelEntry `json:"models"`
}

type providerModelEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	OwnedBy     string `json:"owned_by"`
}

func parseProviderModelsBody(body []byte) ([]ProviderModel, error) {
	var payload providerModelsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		// 少数站点直接返回裸数组。
		var bare []providerModelEntry
		if bareErr := json.Unmarshal(body, &bare); bareErr != nil {
			return nil, errors.New("模型列表响应不是可识别的 JSON 结构")
		}
		payload.Data = bare
	}

	entries := payload.Data
	if len(entries) == 0 {
		entries = payload.Models
	}
	if len(entries) == 0 {
		return nil, errors.New("模型列表为空，请确认密钥与站点地址")
	}

	seen := make(map[string]struct{}, len(entries))
	models := make([]ProviderModel, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, ProviderModel{
			ID:      id,
			Name:    firstNonEmptyTrimmed(entry.DisplayName, entry.Name, id),
			OwnedBy: strings.TrimSpace(entry.OwnedBy),
		})
	}
	if len(models) == 0 {
		return nil, errors.New("模型列表为空，请确认密钥与站点地址")
	}
	sort.Slice(models, func(i int, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
