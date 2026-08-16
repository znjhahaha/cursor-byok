package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"cursor/internal/appdata"
	"cursor/internal/netproxy"
	"cursor/internal/skills"
)

// InstalledSkill 定义本地已安装 skill。
type InstalledSkill = skills.InstalledSkill

// RemoteSkill 定义远端仓库中的 skill。
type RemoteSkill = skills.RemoteSkill

// SkillRepo 定义一个 skills 仓库。
type SkillRepo = skills.Repo

// SkillUpdateStatus 定义单个 skill 的更新检测结果。
type SkillUpdateStatus = skills.SkillUpdateStatus

// SkillsService 定义 skills 管理的 Wails service。
// 安装目录为 ~/.claude/skills：Cursor 客户端与 Claude Code 都会扫描该目录，
// 安装后 Cursor 下一次请求即可携带新 skill，无需重启。
type SkillsService struct {
	manager *skills.Manager
	app     *application.App
	mu      sync.RWMutex
}

// NewSkillsService 创建 skills 管理 service。
func NewSkillsService() *SkillsService {
	installDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		installDir = filepath.Join(home, ".claude", "skills")
	}
	return &SkillsService{
		// 仓库 zip 可能有几十 MB，弱网下载给足超时。
		manager: skills.NewManager(installDir, appdata.DataRootPath(), netproxy.NewHTTPClient(180*time.Second)),
	}
}

// SetApp 注入应用实例，备份/恢复时用于弹出系统文件对话框。
func (service *SkillsService) SetApp(app *application.App) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.app = app
}

// ListInstalledSkills 返回本地已安装的全部 skill。
func (service *SkillsService) ListInstalledSkills() ([]InstalledSkill, error) {
	return service.manager.ListInstalled()
}

// ListSkillRepos 返回内置与自定义 skills 仓库。
func (service *SkillsService) ListSkillRepos() []SkillRepo {
	return service.manager.ListRepos()
}

// AddSkillRepo 添加一个自定义仓库（owner/repo、owner/repo@branch 或 GitHub URL）。
func (service *SkillsService) AddSkillRepo(spec string) (SkillRepo, error) {
	return service.manager.AddRepo(spec)
}

// RemoveSkillRepo 删除一个自定义仓库。
func (service *SkillsService) RemoveSkillRepo(repoID string) error {
	return service.manager.RemoveRepo(repoID)
}

// FetchRemoteSkills 拉取仓库中的 skill 列表；refresh 为 true 时强制重新下载。
func (service *SkillsService) FetchRemoteSkills(repoID string, refresh bool) ([]RemoteSkill, error) {
	return service.manager.FetchRemoteSkills(repoID, refresh)
}

// InstallSkill 安装（或覆盖更新）仓库中的一个 skill。
func (service *SkillsService) InstallSkill(repoID string, subdir string) (InstalledSkill, error) {
	return service.manager.Install(repoID, subdir)
}

// UninstallSkill 永久删除一个已安装 skill。
func (service *SkillsService) UninstallSkill(dirName string) error {
	return service.manager.Uninstall(dirName)
}

// GetRemoteSkillContent 返回远端 skill 的 SKILL.md 全文（安装前预览）。
func (service *SkillsService) GetRemoteSkillContent(repoID string, subdir string) (string, error) {
	return service.manager.GetRemoteSkillContent(repoID, subdir)
}

// GetInstalledSkillContent 返回已安装 skill 的 SKILL.md 全文。
func (service *SkillsService) GetInstalledSkillContent(dirName string) (string, error) {
	return service.manager.GetInstalledSkillContent(dirName)
}

// OpenSkillsDirectory 在文件管理器中打开 skills 安装目录。
func (service *SkillsService) OpenSkillsDirectory() {
	dir := service.manager.InstallDir()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	openDirectory(dir)
}

// CheckSkillUpdates 检测所有带来源记录的已安装 skill 是否有更新。
func (service *SkillsService) CheckSkillUpdates() ([]SkillUpdateStatus, error) {
	return service.manager.CheckSkillUpdates()
}

// BackupSkillsToZip 弹出保存对话框，把全部已安装 skill 打包成 zip。
// 返回最终写入路径；用户取消时返回空字符串且不视为错误。
func (service *SkillsService) BackupSkillsToZip() (string, error) {
	service.mu.RLock()
	app := service.app
	service.mu.RUnlock()

	filename := fmt.Sprintf("skills-backup-%s.zip", time.Now().Format("20060102"))
	targetPath := filepath.Join(appdata.DataRootPath(), "exports", filename)
	if app != nil {
		path, err := app.Dialog.SaveFile().
			SetFilename(filename).
			AddFilter("Zip (*.zip)", "*.zip").
			PromptForSingleSelection()
		if err != nil {
			return "", err
		}
		path = strings.TrimSpace(path)
		if path == "" {
			return "", nil
		}
		targetPath = path
	}
	if _, err := service.manager.BackupSkills(targetPath); err != nil {
		return "", err
	}
	return targetPath, nil
}

// RestoreSkillsFromZip 弹出打开对话框选择备份 zip 并恢复，返回恢复的 skill 数量。
// 用户取消时返回 -1 且不视为错误。
func (service *SkillsService) RestoreSkillsFromZip() (int, error) {
	service.mu.RLock()
	app := service.app
	service.mu.RUnlock()
	if app == nil {
		return 0, fmt.Errorf("文件对话框不可用")
	}
	path, err := app.Dialog.OpenFile().
		AddFilter("Zip (*.zip)", "*.zip").
		PromptForSingleSelection()
	if err != nil {
		return 0, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return -1, nil
	}
	return service.manager.RestoreSkills(path)
}
