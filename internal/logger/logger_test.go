package logger

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwitchableFileHandlerHotSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	handler := newSwitchableFileHandler(path, 1024, 1)
	t.Cleanup(func() { handler.SetEnabled(false) })
	log := slog.New(handler)

	log.Info("disabled-entry")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled handler created log file: %v", err)
	}

	if !handler.SetEnabled(true) || !handler.EnabledForWriting() {
		t.Fatal("handler did not enable")
	}
	log.Info("enabled-entry")
	assertLogFileContains(t, path, "enabled-entry")

	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat enabled log: %v", err)
	}
	if !handler.SetEnabled(false) || handler.EnabledForWriting() || handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("handler did not disable")
	}
	log.Info("disabled-after-entry")
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat disabled log: %v", err)
	}
	if infoAfter.Size() != infoBefore.Size() {
		t.Fatalf("disabled handler changed log size: %d != %d", infoAfter.Size(), infoBefore.Size())
	}

	if !handler.SetEnabled(true) {
		t.Fatal("handler did not re-enable")
	}
	log.Info("reenabled-entry")
	assertLogFileContains(t, path, "reenabled-entry")
}

func TestRollingFileWriterKeepsBoundedBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	handler := newSwitchableFileHandler(path, 80, 2)
	t.Cleanup(func() { handler.SetEnabled(false) })
	handler.SetEnabled(true)
	log := slog.New(handler)
	for index := 0; index < 12; index++ {
		log.Info("rolling-entry", "index", index, "payload", strings.Repeat("x", 24))
	}
	handler.SetEnabled(false)

	for _, candidate := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat rolling log %s: %v", candidate, err)
		}
		if info.Size() == 0 {
			t.Fatalf("rolling log %s is empty", candidate)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected third backup: %v", err)
	}
}

func assertLogFileContains(t *testing.T, path string, needle string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(payload), needle) {
		t.Fatalf("log file does not contain %q: %s", needle, payload)
	}
}
