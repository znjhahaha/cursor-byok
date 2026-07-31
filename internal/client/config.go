package client

import (
	"context"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// UserConfig 定义了当前模块中的 UserConfig 类型。
type UserConfig = serverconfig.Config

type DetailedLoggingState struct {
	Enabled      bool `json:"enabled"`
	Configured   bool `json:"configured"`
	FileEnabled  bool `json:"fileEnabled"`
	DebugEnabled bool `json:"debugEnabled"`
}

// LoadUserConfig 用于处理与 LoadUserConfig 相关的逻辑。
func (s *ProxyService) LoadUserConfig() (UserConfig, error) {
	if s == nil {
		return serverconfig.DefaultConfig(), nil
	}
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	if s.backendHost != nil {
		return s.backendHost.LoadConfig(ctx)
	}
	if s.store == nil {
		return serverconfig.DefaultConfig(), nil
	}
	return s.store.Load(ctx)
}

// SaveUserConfig 用于处理与 SaveUserConfig 相关的逻辑。
func (s *ProxyService) SaveUserConfig(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.SaveConfig(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.Save(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	return nil
}

func (s *ProxyService) GetDetailedLoggingState() (DetailedLoggingState, error) {
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return DetailedLoggingState{}, err
	}
	return s.detailedLoggingState(cfg), nil
}

func (s *ProxyService) SetDetailedLoggingEnabled(value bool) (DetailedLoggingState, error) {
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return DetailedLoggingState{}, err
	}
	cfg.Log = value
	if err := s.SaveUserConfig(cfg); err != nil {
		return DetailedLoggingState{}, err
	}
	// Keep the dedicated switch authoritative even when the service is created
	// without a backend host in diagnostics or recovery paths.
	s.setDetailedFileLoggingEnabled(value)
	persisted, err := s.LoadUserConfig()
	if err != nil {
		return DetailedLoggingState{}, err
	}
	return s.detailedLoggingState(persisted), nil
}

func (s *ProxyService) detailedLoggingState(cfg UserConfig) DetailedLoggingState {
	fileEnabled := s.isDetailedFileLoggingEnabled()
	return DetailedLoggingState{
		Enabled:      cfg.Log && fileEnabled,
		Configured:   cfg.Log,
		FileEnabled:  fileEnabled,
		DebugEnabled: cfg.Log,
	}
}

func (s *ProxyService) emitUserConfigChanged(cfg UserConfig) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit("user-config:changed", cfg)
}

// resolveUserConfigPath 用于处理与 resolveUserConfigPath 相关的逻辑。
func resolveUserConfigPath() string {
	return appdata.ConfigFilePath()
}

// resolveLogsRootPath 用于处理与 resolveLogsRootPath 相关的逻辑。
func resolveLogsRootPath() string {
	return appdata.LogsRootPath()
}

// ResolveLogsRootPath 用于处理与 ResolveLogsRootPath 相关的逻辑。
func ResolveLogsRootPath() string {
	return resolveLogsRootPath()
}

// ResolveSettingsRootPath 用于处理与 ResolveSettingsRootPath 相关的逻辑。
func ResolveSettingsRootPath() string {
	return appdata.RootDir()
}
