package config

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Store struct {
	path     string
	logsRoot string
	mu       sync.Mutex
}

type fileSnapshot struct {
	exists  bool
	modTime int64
	size    int64
}

func NewStore(path string, logsRoot string) *Store {
	return &Store{
		path:     strings.TrimSpace(path),
		logsRoot: strings.TrimSpace(logsRoot),
	}
}

func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

func (store *Store) LogsRoot() string {
	if store == nil {
		return ""
	}
	return store.logsRoot
}

func (store *Store) snapshot() fileSnapshot {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return fileSnapshot{}
	}
	info, err := os.Stat(store.path)
	if err != nil {
		return fileSnapshot{}
	}
	return fileSnapshot{
		exists:  true,
		modTime: info.ModTime().UnixNano(),
		size:    info.Size(),
	}
}

func (store *Store) Load(_ context.Context) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return DefaultConfig(), nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			defaultConfig := DefaultConfig()
			if err := store.saveLocked(defaultConfig); err != nil {
				return DefaultConfig(), err
			}
			return defaultConfig, nil
		}
		return DefaultConfig(), fmt.Errorf("读取用户配置失败: %w", err)
	}

	var current Config
	if err := yaml.Unmarshal(data, &current); err != nil {
		return DefaultConfig(), fmt.Errorf("解析用户配置失败: %w", err)
	}
	normalized, err := NormalizeConfig(current)
	if err != nil {
		return DefaultConfig(), err
	}
	if shouldPersistNormalizedConfig(data, current, normalized) {
		if err := store.saveLocked(normalized); err != nil {
			return DefaultConfig(), err
		}
	}
	return normalized, nil
}

func (store *Store) Save(_ context.Context, cfg Config) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, errors.New("配置存储未初始化")
	}

	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return Config{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if err := store.saveLocked(normalized); err != nil {
		log.Printf("config save failed path=%s error=%v", store.path, err)
		return Config{}, err
	}
	if err := store.verifyPersistedLocked(normalized); err != nil {
		log.Printf("config save verify failed path=%s error=%v", store.path, err)
		return Config{}, err
	}
	log.Printf("config saved path=%s providers=%d adapters=%d", store.path, len(normalized.Providers), len(normalized.ModelAdapters))
	return normalized, nil
}

// verifyPersistedLocked 回读刚落盘的文件并比对关键字段。
// 把「写盘成功但内容不完整」（如 apiKey 被上游静默剥掉）变成显式错误，
// 避免用户关掉 exe 后才发现配置丢失。
func (store *Store) verifyPersistedLocked(expected Config) error {
	data, err := os.ReadFile(store.path)
	if err != nil {
		return fmt.Errorf("回读用户配置失败: %w", err)
	}
	var persisted Config
	if err := yaml.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("回读用户配置解析失败: %w", err)
	}
	if len(persisted.Providers) != len(expected.Providers) {
		return fmt.Errorf("中转站数量不一致: 写入 %d 个, 回读 %d 个", len(expected.Providers), len(persisted.Providers))
	}
	if len(persisted.ModelAdapters) != len(expected.ModelAdapters) {
		return fmt.Errorf("模型适配器数量不一致: 写入 %d 个, 回读 %d 个", len(expected.ModelAdapters), len(persisted.ModelAdapters))
	}
	persistedProviders := make(map[string]ProviderConfig, len(persisted.Providers))
	for _, item := range persisted.Providers {
		persistedProviders[item.ID] = item
	}
	for _, expectedProvider := range expected.Providers {
		persistedProvider, ok := persistedProviders[expectedProvider.ID]
		if !ok {
			return fmt.Errorf("中转站 %s 未能持久化", expectedProvider.ID)
		}
		if strings.TrimSpace(persistedProvider.APIKey) != strings.TrimSpace(expectedProvider.APIKey) {
			return fmt.Errorf("中转站 %s 的 apiKey 未能完整持久化", expectedProvider.ID)
		}
	}
	for index, expectedAdapter := range expected.ModelAdapters {
		persistedAdapter := persisted.ModelAdapters[index]
		if strings.TrimSpace(persistedAdapter.ProviderID) != strings.TrimSpace(expectedAdapter.ProviderID) ||
			strings.TrimSpace(persistedAdapter.DisplayName) != strings.TrimSpace(expectedAdapter.DisplayName) ||
			strings.TrimSpace(persistedAdapter.ModelID) != strings.TrimSpace(expectedAdapter.ModelID) ||
			strings.TrimSpace(persistedAdapter.APIKey) != strings.TrimSpace(expectedAdapter.APIKey) {
			return fmt.Errorf("第 %d 个模型适配器未能完整持久化", index+1)
		}
	}
	return nil
}

func (store *Store) saveLocked(normalized Config) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return fmt.Errorf("创建用户配置目录失败: %w", err)
	}

	data, err := yaml.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化用户配置失败: %w", err)
	}

	tempPath := store.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := os.Rename(tempPath, store.path); err != nil {
		return fmt.Errorf("保存用户配置失败: %w", err)
	}
	return nil
}

func shouldPersistNormalizedConfig(raw []byte, current Config, normalized Config) bool {
	if !yamlHasKey(raw, "backendListenAddr") || !yamlHasKey(raw, "proxyListenAddr") {
		return true
	}
	if current.BackendListenAddr != normalized.BackendListenAddr || current.ProxyListenAddr != normalized.ProxyListenAddr {
		return true
	}
	if current.ProviderStreamIdleTimeout == normalized.ProviderStreamIdleTimeout {
		return false
	}
	return yamlHasKey(raw, "providerStreamIdleTimeout")
}

func yamlHasKey(raw []byte, key string) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return false
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return false
	}
	mapping := root.Content[0]
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return true
		}
	}
	return false
}
