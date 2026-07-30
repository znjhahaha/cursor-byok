package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	serverconfig "cursor/internal/backend/server/config"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	providerModelsUpdatedEvent      = "provider-models:updated"
	providerModelsCacheSchemaVer    = 1
	providerModelsRequestHashLength = 16
)

// ProviderModelsCachePayload 用于向所有窗口广播当前模型列表缓存快照。
//
// 三个配置窗口是独立 WebviewWindow，各有独立 JS 堆；缓存的唯一真值在 Go 侧，
// 靠这个事件让各窗口保持一致，避免每个窗口各拉一次。
type ProviderModelsCachePayload struct {
	Results []ProviderModelsResult `json:"results"`
}

// providerModelsCacheState 是落盘结构，key 为请求内容哈希。
type providerModelsCacheState struct {
	SchemaVersion int                             `json:"schemaVersion"`
	Entries       map[string]ProviderModelsResult `json:"entries"`
}

// providerModelsCache 是进程内唯一的模型列表缓存，并同步落盘。
//
// 落盘布局与写入方式沿用 forwarder/docs_index_store.go 的既有约定：
// 懒加载 + 临时文件原子替换 + 0600 权限。
type providerModelsCache struct {
	mu     sync.Mutex
	path   string
	loaded bool
	state  providerModelsCacheState
}

func newProviderModelsCache(path string) *providerModelsCache {
	return &providerModelsCache{path: strings.TrimSpace(path)}
}

// buildProviderModelsRequestHash 计算一次模型列表请求的内容指纹。
//
// 除 Name/Note/Builtin/HomeURL 这些纯展示字段外全部参与：其余任一字段变化都会
// 改变实际发出的请求（BaseURL/ModelsPath 决定 URL，Type/APIKey/UserAgent/HeadersJSON
// 决定请求头，InferencePath 决定是否还需要探测推理端点）。
//
// 不能用 provider.ID 当缓存键：buildProviderID 只哈希 baseURL 与名称，
// 以保证用户改密钥时 adapter 的引用关系不断裂，因此改完密钥 ID 不变，
// 拿它当键会命中旧密钥的结果。
func buildProviderModelsRequestHash(provider serverconfig.ProviderConfig) string {
	parts := []string{
		strings.TrimSpace(provider.Type),
		strings.TrimSpace(provider.BaseURL),
		strings.TrimSpace(provider.APIKey),
		strings.TrimSpace(provider.ClientProfile),
		strings.TrimSpace(provider.UserAgent),
		strings.TrimSpace(provider.HeadersJSON),
		strings.TrimSpace(provider.ModelsPath),
		strings.TrimSpace(provider.InferencePath),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:providerModelsRequestHashLength]
}

// Get 按内容哈希取缓存，只返回成功结果。
func (cache *providerModelsCache) Get(requestHash string) (ProviderModelsResult, bool) {
	requestHash = strings.TrimSpace(requestHash)
	if cache == nil || requestHash == "" {
		return ProviderModelsResult{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := cache.loadLocked(); err != nil {
		return ProviderModelsResult{}, false
	}
	result, ok := cache.state.Entries[requestHash]
	if !ok || !result.OK {
		return ProviderModelsResult{}, false
	}
	return result, true
}

// Put 写入一次成功的拉取结果。
//
// 只接受 OK 结果：失败往往是一次网络抖动或临时鉴权问题，缓存下来会让用户
// 在修好配置之后仍然看到旧错误。
func (cache *providerModelsCache) Put(result ProviderModelsResult) error {
	if cache == nil || !result.OK {
		return nil
	}
	requestHash := strings.TrimSpace(result.RequestHash)
	if requestHash == "" {
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := cache.loadLocked(); err != nil {
		return err
	}
	cache.state.Entries[requestHash] = result
	return cache.saveLocked()
}

// Snapshot 返回全部缓存条目，按拉取时间倒序。
func (cache *providerModelsCache) Snapshot() []ProviderModelsResult {
	if cache == nil {
		return []ProviderModelsResult{}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := cache.loadLocked(); err != nil {
		return []ProviderModelsResult{}
	}
	results := make([]ProviderModelsResult, 0, len(cache.state.Entries))
	for _, item := range cache.state.Entries {
		results = append(results, item)
	}
	sort.Slice(results, func(i int, j int) bool {
		if results[i].FetchedAt == results[j].FetchedAt {
			return results[i].RequestHash < results[j].RequestHash
		}
		return results[i].FetchedAt > results[j].FetchedAt
	})
	return results
}

func (cache *providerModelsCache) loadLocked() error {
	if cache.loaded {
		return nil
	}
	state := providerModelsCacheState{
		SchemaVersion: providerModelsCacheSchemaVer,
		Entries:       make(map[string]ProviderModelsResult),
	}
	data, err := os.ReadFile(cache.path)
	if err != nil {
		if os.IsNotExist(err) {
			cache.state = state
			cache.loaded = true
			return nil
		}
		return err
	}
	if len(data) != 0 {
		if err := json.Unmarshal(data, &state); err != nil {
			// 缓存文件损坏不应该阻断功能：丢掉它，回到「未命中」即可。
			state = providerModelsCacheState{
				SchemaVersion: providerModelsCacheSchemaVer,
				Entries:       make(map[string]ProviderModelsResult),
			}
		}
	}
	if state.SchemaVersion <= 0 {
		state.SchemaVersion = providerModelsCacheSchemaVer
	}
	if state.Entries == nil {
		state.Entries = make(map[string]ProviderModelsResult)
	}
	cache.state = state
	cache.loaded = true
	return nil
}

func (cache *providerModelsCache) saveLocked() error {
	root := filepath.Dir(cache.path)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".provider-models-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 结果里不含明文密钥（密钥只出现在请求头，不进 URL），但仍按 history store
	// 的既有约定收紧权限。
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, cache.path)
}

// GetProviderModelsCache 返回当前配置命中的模型列表缓存，不发起任何网络请求。
//
// 只回传哈希与当前 provider 配置一致的条目：换过密钥或地址的旧条目会被过滤掉，
// 前端因此不需要自己算哈希，也不会展示到过期数据。
func (s *ProxyService) GetProviderModelsCache() (ProviderModelsCachePayload, error) {
	payload := ProviderModelsCachePayload{Results: []ProviderModelsResult{}}
	if s == nil || s.providerModels == nil {
		return payload, nil
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return payload, err
	}
	for _, provider := range cfg.Providers {
		normalized, normalizeErr := serverconfig.NormalizeProviderConfig(provider)
		if normalizeErr != nil {
			continue
		}
		result, ok := s.providerModels.Get(buildProviderModelsRequestHash(normalized))
		if !ok {
			continue
		}
		payload.Results = append(payload.Results, result)
	}
	return payload, nil
}

// storeAndEmitProviderModels 落盘一次成功结果并向所有窗口广播快照。
//
// 会写入两个哈希：本次请求用的那个，以及把探测结果物化回配置之后的那个。
// 详见 effectiveProviderAfterProbe 的说明。
func (s *ProxyService) storeAndEmitProviderModels(requested serverconfig.ProviderConfig, result ProviderModelsResult) {
	if s == nil || s.providerModels == nil || !result.OK {
		return
	}
	if err := s.providerModels.Put(result); err != nil {
		// 落盘失败不影响本次返回值，下次拉取会重试。
		return
	}
	// 必须先归一化再哈希：GetProviderModelsCache 查的是归一化后的配置，
	// 路径的 trailing slash 差异会让两边算出不同的哈希，缓存就白存了。
	effective, normalizeErr := serverconfig.NormalizeProviderConfig(effectiveProviderAfterProbe(requested, result))
	if normalizeErr == nil {
		effectiveHash := buildProviderModelsRequestHash(effective)
		if effectiveHash != result.RequestHash {
			mirrored := result
			mirrored.RequestHash = effectiveHash
			if err := s.providerModels.Put(mirrored); err != nil {
				return
			}
		}
	}
	s.emitProviderModelsCache()
}

func (s *ProxyService) emitProviderModelsCache() {
	app := application.Get()
	if app == nil {
		return
	}
	payload, err := s.GetProviderModelsCache()
	if err != nil {
		return
	}
	app.Event.Emit(providerModelsUpdatedEvent, payload)
}
