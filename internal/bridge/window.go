package bridge

import (
	"cursor/internal/buildinfo"
	"cursor/internal/client"
	"cursor/internal/updater"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"sync"

	"github.com/leaanthony/u"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// modelEditorContext 保存当前模型编辑器窗口的初始化上下文。
type modelEditorContext struct {
	Index       int    `json:"index"`
	AdapterJSON string `json:"adapterJSON"`
}

// providerEditorContext 保存当前中转站编辑器窗口的初始化上下文。
// 中转站按 id 定位而非数组下标，因此这里不需要 index。
type providerEditorContext struct {
	ProviderJSON string `json:"providerJSON"`
}

// WindowService 定义了当前模块中的 WindowService 类型。
type WindowService struct {
	app                  *application.App
	updater              *updater.Manager
	modelConfigWindow    *application.WebviewWindow
	modelEditorWindow    *application.WebviewWindow
	providerEditorWindow *application.WebviewWindow
	editorCtx            *modelEditorContext
	providerCtx          *providerEditorContext
	mu                   sync.RWMutex
}

// NewWindowService 用于处理与 NewWindowService 相关的逻辑。
func NewWindowService() *WindowService {
	return &WindowService{}
}

// SetApp 用于处理与 SetApp 相关的逻辑。
func (s *WindowService) SetApp(app *application.App) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.app = app
}

// SetUpdater 关联更新管理器，供前端手动触发检查更新。
func (s *WindowService) SetUpdater(manager *updater.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updater = manager
}

// GetAppVersion 返回当前应用版本号。
func (s *WindowService) GetAppVersion() string {
	return buildinfo.CurrentVersion()
}

// CheckForUpdates 触发一次手动检查更新。
func (s *WindowService) CheckForUpdates() {
	s.mu.RLock()
	manager := s.updater
	s.mu.RUnlock()
	if manager == nil {
		return
	}
	manager.CheckNow(true)
}

// InstallReadyUpdate 安装当前已下载完成的更新。
func (s *WindowService) InstallReadyUpdate() error {
	s.mu.RLock()
	manager := s.updater
	s.mu.RUnlock()
	if manager == nil {
		return fmt.Errorf("更新管理器未初始化")
	}
	return manager.InstallReadyUpdate()
}

// OpenConfigWindow 打开本地设置目录。
func (s *WindowService) OpenConfigWindow() {
	_ = os.MkdirAll(client.ResolveSettingsRootPath(), 0o755)
	openDirectory(client.ResolveSettingsRootPath())
}

// childWindowSpec 描述子窗口之间真正有差异的部分。
// 其余外观选项由 buildChildWindowOptions 统一给出，避免每新增一个窗口就复制一整块配置。
type childWindowSpec struct {
	Title      string
	URL        string
	Width      int
	Height     int
	MinWidth   int
	MinHeight  int
	Fullscreen bool
}

func buildChildWindowOptions(spec childWindowSpec) application.WebviewWindowOptions {
	fullscreen := u.False
	if spec.Fullscreen {
		fullscreen = u.True
	}
	return application.WebviewWindowOptions{
		Title:               spec.Title,
		Width:               spec.Width,
		Height:              spec.Height,
		MinWidth:            spec.MinWidth,
		MinHeight:           spec.MinHeight,
		DisableResize:       false,
		Frameless:           goruntime.GOOS == "windows",
		URL:                 spec.URL,
		Hidden:              false,
		HideOnEscape:        false,
		MinimiseButtonState: application.ButtonEnabled,
		MaximiseButtonState: application.ButtonEnabled,
		CloseButtonState:    application.ButtonEnabled,
		BackgroundColour:    application.RGBA{Red: 25, Green: 25, Blue: 25, Alpha: 255},
		Mac: application.MacWindow{
			Backdrop:      application.MacBackdropLiquidGlass,
			DisableShadow: false,
			TitleBar: application.MacTitleBar{
				AppearsTransparent:   true,
				Hide:                 false,
				HideTitle:            true,
				FullSizeContent:      true,
				UseToolbar:           false,
				HideToolbarSeparator: true,
			},
			WebviewPreferences: application.MacWebviewPreferences{
				FullscreenEnabled:                   fullscreen,
				TextInteractionEnabled:              u.True,
				AllowsBackForwardNavigationGestures: u.False,
			},
		},
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: false,
		},
	}
}

// OpenModelConfigWindow 打开模型配置独立窗口。如果窗口已存在则聚焦。
func (s *WindowService) OpenModelConfigWindow() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.app == nil {
		return
	}

	if s.modelConfigWindow != nil {
		s.modelConfigWindow.Show()
		s.modelConfigWindow.Focus()
		return
	}

	win := s.app.Window.NewWithOptions(buildChildWindowOptions(childWindowSpec{
		Title:      "模型配置",
		URL:        "/#/model-config",
		Width:      980,
		Height:     700,
		MinWidth:   820,
		MinHeight:  560,
		Fullscreen: true,
	}))

	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.modelConfigWindow = nil
	})

	s.modelConfigWindow = win
}

// OpenModelEditorWindow 打开模型编辑器独立窗口。
// index < 0 表示新增，>= 0 表示编辑对应索引的适配器。
// adapterJSON 为编辑器初始数据的 JSON 字符串。
func (s *WindowService) OpenModelEditorWindow(index int, adapterJSON string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.app == nil {
		return
	}

	s.editorCtx = &modelEditorContext{
		Index:       index,
		AdapterJSON: adapterJSON,
	}

	if s.modelEditorWindow != nil {
		s.modelEditorWindow.Show()
		s.modelEditorWindow.Focus()
		return
	}

	title := "新增模型配置"
	if index >= 0 {
		title = "编辑模型配置"
	}

	win := s.app.Window.NewWithOptions(buildChildWindowOptions(childWindowSpec{
		Title:     title,
		URL:       fmt.Sprintf("/#/model-editor?index=%d", index),
		Width:     840,
		Height:    680,
		MinWidth:  740,
		MinHeight: 600,
	}))

	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.modelEditorWindow = nil
		s.editorCtx = nil
	})

	s.modelEditorWindow = win
}

// OpenProviderEditorWindow 打开中转站编辑独立窗口。
// providerJSON 为空对象时表示新增；带 id 时表示编辑既有站点。
func (s *WindowService) OpenProviderEditorWindow(providerJSON string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.app == nil {
		return
	}

	s.providerCtx = &providerEditorContext{ProviderJSON: providerJSON}

	if s.providerEditorWindow != nil {
		s.providerEditorWindow.Show()
		s.providerEditorWindow.Focus()
		return
	}

	win := s.app.Window.NewWithOptions(buildChildWindowOptions(childWindowSpec{
		Title:     "中转站配置",
		URL:       "/#/provider-editor",
		Width:     880,
		Height:    720,
		MinWidth:  760,
		MinHeight: 620,
	}))

	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.providerEditorWindow = nil
		s.providerCtx = nil
	})

	s.providerEditorWindow = win
}

// GetProviderEditorContext 返回当前中转站编辑窗口的初始化上下文。
func (s *WindowService) GetProviderEditorContext() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.providerCtx == nil {
		return map[string]any{"providerJSON": "{}"}
	}
	return map[string]any{"providerJSON": s.providerCtx.ProviderJSON}
}

// GetModelEditorContext 返回当前编辑器窗口的初始化上下文。
func (s *WindowService) GetModelEditorContext() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.editorCtx == nil {
		return map[string]any{
			"index":       -1,
			"adapterJSON": "{}",
		}
	}
	return map[string]any{
		"index":       s.editorCtx.Index,
		"adapterJSON": s.editorCtx.AdapterJSON,
	}
}

// OpenHistoryWindow 用于处理与 OpenHistoryWindow 相关的逻辑。
func (s *WindowService) OpenHistoryWindow() {
	_ = os.MkdirAll(client.ResolveLogsRootPath(), 0o755)
	openDirectory(client.ResolveLogsRootPath())
}

// openDirectory 用于处理与 openDirectory 相关的逻辑。
func openDirectory(path string) {
	if path == "" {
		return
	}
	switch goruntime.GOOS {
	case "darwin":
		_ = exec.Command("open", path).Start()
	case "windows":
		_ = exec.Command("explorer", path).Start()
	default:
		_ = exec.Command("xdg-open", path).Start()
	}
}
