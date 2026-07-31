package client

import (
	"context"
	"path/filepath"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

func TestDetailedLoggingSwitchPersistsAndReportsEffectiveState(t *testing.T) {
	root := t.TempDir()
	store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	if _, err := store.Load(context.Background()); err != nil {
		t.Fatalf("initialize config store: %v", err)
	}

	fileEnabled := false
	var transitions []bool
	service := &ProxyService{
		store: store,
		setFileLoggingEnabled: func(value bool) {
			fileEnabled = value
			transitions = append(transitions, value)
		},
		fileLoggingEnabled: func() bool { return fileEnabled },
	}

	enabled, err := service.SetDetailedLoggingEnabled(true)
	if err != nil {
		t.Fatalf("enable detailed logging: %v", err)
	}
	if !enabled.Enabled || !enabled.Configured || !enabled.FileEnabled || !enabled.DebugEnabled {
		t.Fatalf("enabled state = %#v", enabled)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("reload enabled config: %v", err)
	}
	if !persisted.Log {
		t.Fatal("enabled logging was not persisted")
	}

	disabled, err := service.SetDetailedLoggingEnabled(false)
	if err != nil {
		t.Fatalf("disable detailed logging: %v", err)
	}
	if disabled.Enabled || disabled.Configured || disabled.FileEnabled || disabled.DebugEnabled {
		t.Fatalf("disabled state = %#v", disabled)
	}
	persisted, err = store.Load(context.Background())
	if err != nil {
		t.Fatalf("reload disabled config: %v", err)
	}
	if persisted.Log {
		t.Fatal("disabled logging was not persisted")
	}
	if len(transitions) != 2 || !transitions[0] || transitions[1] {
		t.Fatalf("file logging transitions = %#v", transitions)
	}
}

func TestDetailedLoggingStateDetectsBackendMismatch(t *testing.T) {
	service := &ProxyService{fileLoggingEnabled: func() bool { return false }}
	state := service.detailedLoggingState(UserConfig{Log: true})
	if state.Enabled || !state.Configured || state.FileEnabled || !state.DebugEnabled {
		t.Fatalf("mismatched state = %#v", state)
	}
}
