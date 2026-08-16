// Package skills 管理 Cursor 客户端扫描的用户级 skills 目录：
// 列出已安装 skill、从 GitHub 仓库浏览与安装、卸载，以及自定义仓库的持久化。
// 安装目录固定为 ~/.claude/skills/，该目录同时被 Cursor 与 Claude Code 扫描。
package skills

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	installedRecordFileName = "skills-installed.json"
	customReposFileName     = "skills-repos.json"
	// installStagingSuffix / installBackupSuffix 是安装过程的临时目录后缀，
	// 扫描已安装列表时必须跳过，避免中断残留被当成 skill。
	installStagingSuffix = ".installing"
	installBackupSuffix  = ".skill-backup"
)

// isTransientSkillDir 判断目录是否为安装过程的临时目录。
func isTransientSkillDir(name string) bool {
	return strings.HasSuffix(name, installStagingSuffix) || strings.HasSuffix(name, installBackupSuffix)
}

// replaceDirWithStaging 用 staging 目录替换 targetDir，采用备份式替换而非软链接：
// Windows 创建目录 symlink 需要特权，且 Cursor 对 symlink 目录的扫描行为不可控。
// 旧版本先改名保留，新版本就位后才清理备份；任何一步失败都把旧版本改名还原，
// 全程不存在"旧的已删、新的没就位"的丢失窗口。成功或失败都会清理 staging。
func replaceDirWithStaging(targetDir string, stagingDir string) error {
	backupDir := targetDir + installBackupSuffix
	_ = os.RemoveAll(backupDir)
	hadExisting := false
	if _, err := os.Stat(targetDir); err == nil {
		hadExisting = true
		if err := os.Rename(targetDir, backupDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return fmt.Errorf("backup existing skill: %w", err)
		}
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		if hadExisting {
			_ = os.Rename(backupDir, targetDir)
		}
		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("move skill into place: %w", err)
	}
	if hadExisting {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

// InstalledSkill 是本地已安装 skill 的描述。
type InstalledSkill struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Path        string    `json:"path"`
	SourceRepo  string    `json:"sourceRepo,omitempty"`
	SourcePath  string    `json:"sourcePath,omitempty"`
	InstalledAt time.Time `json:"installedAt,omitempty"`
}

// RemoteSkill 是远端仓库里发现的一个 skill。
type RemoteSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Subdir 是 skill 在仓库内的相对目录，空字符串表示整个仓库就是一个 skill。
	Subdir string `json:"subdir"`
	// Installed 表示本地已存在同名 skill 目录。
	Installed bool `json:"installed"`
	// UpdateAvailable 表示本地已安装且远端 SKILL.md 内容与本地不同。
	UpdateAvailable bool `json:"updateAvailable"`
}

// Repo 是一个可浏览的 skills 仓库。
type Repo struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Branch  string `json:"branch,omitempty"`
	BuiltIn bool   `json:"builtIn"`
}

type installedRecord struct {
	Repo        string    `json:"repo"`
	Subdir      string    `json:"subdir"`
	InstalledAt time.Time `json:"installedAt"`
}

type installedRecordFile struct {
	Skills map[string]installedRecord `json:"skills"`
}

type customReposFile struct {
	Repos []Repo `json:"repos"`
}

// Manager 是 skills 管理核心。安装目录与数据目录在构造时注入。
type Manager struct {
	installDir string
	dataDir    string
	httpClient *http.Client

	mu       sync.Mutex
	archives map[string]*repoArchive
}

// NewManager 创建 skills 管理器。
func NewManager(installDir string, dataDir string, httpClient *http.Client) *Manager {
	return &Manager{
		installDir: strings.TrimSpace(installDir),
		dataDir:    strings.TrimSpace(dataDir),
		httpClient: httpClient,
		archives:   make(map[string]*repoArchive),
	}
}

// InstallDir 返回 skill 安装目录。
func (manager *Manager) InstallDir() string {
	return manager.installDir
}

// ListInstalled 扫描安装目录，返回全部含 SKILL.md 的子目录。
func (manager *Manager) ListInstalled() ([]InstalledSkill, error) {
	entries, err := os.ReadDir(manager.installDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []InstalledSkill{}, nil
		}
		return nil, fmt.Errorf("scan skills directory: %w", err)
	}
	records := manager.loadInstalledRecords()
	skills := make([]InstalledSkill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || isTransientSkillDir(entry.Name()) {
			continue
		}
		dirName := entry.Name()
		skillDir := filepath.Join(manager.installDir, dirName)
		body, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		if err != nil {
			continue
		}
		meta := parseSkillMarkdown(string(body))
		name := meta.Name
		if name == "" {
			name = dirName
		}
		skill := InstalledSkill{
			Name:        name,
			Description: meta.Description,
			Path:        skillDir,
		}
		if record, ok := records[dirName]; ok {
			skill.SourceRepo = record.Repo
			skill.SourcePath = record.Subdir
			skill.InstalledAt = record.InstalledAt
		}
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(left, right int) bool {
		return strings.ToLower(skills[left].Name) < strings.ToLower(skills[right].Name)
	})
	return skills, nil
}

// ListRepos 返回内置仓库与用户自定义仓库。
func (manager *Manager) ListRepos() []Repo {
	repos := []Repo{
		{ID: "anthropics/skills", Owner: "anthropics", Name: "skills", BuiltIn: true},
		{ID: "obra/superpowers", Owner: "obra", Name: "superpowers", BuiltIn: true},
	}
	custom := manager.loadCustomRepos()
	repos = append(repos, custom...)
	return repos
}

// AddRepo 解析仓库描述（owner/repo、owner/repo@branch 或 GitHub URL）并持久化。
func (manager *Manager) AddRepo(spec string) (Repo, error) {
	repo, err := parseRepoSpec(spec)
	if err != nil {
		return Repo{}, err
	}
	for _, existing := range manager.ListRepos() {
		if existing.ID == repo.ID {
			return existing, nil
		}
	}
	custom := manager.loadCustomRepos()
	custom = append(custom, repo)
	if err := manager.saveCustomRepos(custom); err != nil {
		return Repo{}, err
	}
	return repo, nil
}

// RemoveRepo 删除一个自定义仓库；内置仓库不可删除。
func (manager *Manager) RemoveRepo(repoID string) error {
	repoID = strings.TrimSpace(repoID)
	custom := manager.loadCustomRepos()
	next := make([]Repo, 0, len(custom))
	removed := false
	for _, repo := range custom {
		if repo.ID == repoID {
			removed = true
			continue
		}
		next = append(next, repo)
	}
	if !removed {
		return fmt.Errorf("仓库 %q 不存在或为内置仓库", repoID)
	}
	manager.mu.Lock()
	delete(manager.archives, repoID)
	manager.mu.Unlock()
	return manager.saveCustomRepos(next)
}

// FetchRemoteSkills 下载（或复用缓存的）仓库归档并扫描其中全部 skill。
// refresh 为 true 时强制重新下载。
func (manager *Manager) FetchRemoteSkills(repoID string, refresh bool) ([]RemoteSkill, error) {
	repo, err := manager.findRepo(repoID)
	if err != nil {
		return nil, err
	}
	archive, err := manager.repoArchive(repo, refresh)
	if err != nil {
		return nil, err
	}
	installedDirs := manager.installedDirNames()
	remote := archive.scanSkills()
	for index := range remote {
		dirName := remoteSkillDirName(remote[index])
		actualDir, exists := installedDirs[strings.ToLower(dirName)]
		remote[index].Installed = exists
		if !exists {
			continue
		}
		// 已安装的顺带比对 SKILL.md：归档在内存里，读一次成本可忽略，
		// 浏览列表就能直接看出"有更新"而不用单独跑检查。
		remoteBody, err := archive.readFile(joinArchivePath(remote[index].Subdir, "SKILL.md"))
		if err != nil {
			continue
		}
		localBody, err := os.ReadFile(filepath.Join(manager.installDir, actualDir, "SKILL.md"))
		if err != nil {
			remote[index].UpdateAvailable = true
			continue
		}
		remote[index].UpdateAvailable = !skillContentEqual(localBody, remoteBody)
	}
	return remote, nil
}

// Install 把仓库中 subdir 对应的 skill 解压到安装目录，已存在时整目录覆盖。
func (manager *Manager) Install(repoID string, subdir string) (InstalledSkill, error) {
	repo, err := manager.findRepo(repoID)
	if err != nil {
		return InstalledSkill{}, err
	}
	archive, err := manager.repoArchive(repo, false)
	if err != nil {
		return InstalledSkill{}, err
	}
	subdir = strings.Trim(strings.TrimSpace(subdir), "/")
	skillBody, err := archive.readFile(joinArchivePath(subdir, "SKILL.md"))
	if err != nil {
		return InstalledSkill{}, fmt.Errorf("仓库中未找到 %s 的 SKILL.md: %w", subdir, err)
	}
	meta := parseSkillMarkdown(string(skillBody))
	dirName := sanitizeSkillDirName(firstNonEmptyString(meta.Name, lastPathSegment(subdir), repo.Name))
	if dirName == "" {
		return InstalledSkill{}, fmt.Errorf("无法确定 skill 目录名")
	}
	targetDir := filepath.Join(manager.installDir, dirName)

	stagingDir := targetDir + installStagingSuffix
	_ = os.RemoveAll(stagingDir)
	if err := archive.extractDir(subdir, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstalledSkill{}, err
	}
	if err := replaceDirWithStaging(targetDir, stagingDir); err != nil {
		return InstalledSkill{}, err
	}

	now := time.Now()
	records := manager.loadInstalledRecords()
	records[dirName] = installedRecord{Repo: repo.ID, Subdir: subdir, InstalledAt: now}
	if err := manager.saveInstalledRecords(records); err != nil {
		return InstalledSkill{}, err
	}
	return InstalledSkill{
		Name:        firstNonEmptyString(meta.Name, dirName),
		Description: meta.Description,
		Path:        targetDir,
		SourceRepo:  repo.ID,
		SourcePath:  subdir,
		InstalledAt: now,
	}, nil
}

// GetRemoteSkillContent 返回仓库中某个 skill 的 SKILL.md 全文，供安装前预览。
// 优先复用已缓存的归档，未拉取过时会触发一次下载。
func (manager *Manager) GetRemoteSkillContent(repoID string, subdir string) (string, error) {
	repo, err := manager.findRepo(repoID)
	if err != nil {
		return "", err
	}
	archive, err := manager.repoArchive(repo, false)
	if err != nil {
		return "", err
	}
	subdir = strings.Trim(strings.TrimSpace(subdir), "/")
	body, err := archive.readFile(joinArchivePath(subdir, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("仓库中未找到 %s 的 SKILL.md: %w", subdir, err)
	}
	return string(body), nil
}

// GetInstalledSkillContent 返回本地已安装 skill 的 SKILL.md 全文。
func (manager *Manager) GetInstalledSkillContent(dirName string) (string, error) {
	dirName = sanitizeSkillDirName(dirName)
	if dirName == "" {
		return "", fmt.Errorf("skill 名称无效")
	}
	body, err := os.ReadFile(filepath.Join(manager.installDir, dirName, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("读取 SKILL.md 失败: %w", err)
	}
	return string(body), nil
}

// Uninstall 永久删除一个已安装 skill 目录。
func (manager *Manager) Uninstall(dirName string) error {
	dirName = sanitizeSkillDirName(dirName)
	if dirName == "" {
		return fmt.Errorf("skill 名称无效")
	}
	targetDir := filepath.Join(manager.installDir, dirName)
	if _, err := os.Stat(filepath.Join(targetDir, "SKILL.md")); err != nil {
		return fmt.Errorf("skill %q 不存在", dirName)
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("remove skill: %w", err)
	}
	records := manager.loadInstalledRecords()
	if _, ok := records[dirName]; ok {
		delete(records, dirName)
		_ = manager.saveInstalledRecords(records)
	}
	return nil
}

func (manager *Manager) findRepo(repoID string) (Repo, error) {
	repoID = strings.TrimSpace(repoID)
	for _, repo := range manager.ListRepos() {
		if repo.ID == repoID {
			return repo, nil
		}
	}
	return Repo{}, fmt.Errorf("仓库 %q 未添加", repoID)
}

// installedDirNames 返回小写目录名到实际目录名的映射，
// 值用于在区分大小写的文件系统上按真实名字读取文件。
func (manager *Manager) installedDirNames() map[string]string {
	names := make(map[string]string)
	entries, err := os.ReadDir(manager.installDir)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if entry.IsDir() && !isTransientSkillDir(entry.Name()) {
			names[strings.ToLower(entry.Name())] = entry.Name()
		}
	}
	return names
}

func remoteSkillDirName(skill RemoteSkill) string {
	return sanitizeSkillDirName(firstNonEmptyString(skill.Name, lastPathSegment(skill.Subdir)))
}

func (manager *Manager) loadInstalledRecords() map[string]installedRecord {
	var file installedRecordFile
	body, err := os.ReadFile(filepath.Join(manager.dataDir, installedRecordFileName))
	if err == nil {
		_ = json.Unmarshal(body, &file)
	}
	if file.Skills == nil {
		file.Skills = make(map[string]installedRecord)
	}
	return file.Skills
}

func (manager *Manager) saveInstalledRecords(records map[string]installedRecord) error {
	return writeJSONFile(filepath.Join(manager.dataDir, installedRecordFileName), installedRecordFile{Skills: records})
}

func (manager *Manager) loadCustomRepos() []Repo {
	var file customReposFile
	body, err := os.ReadFile(filepath.Join(manager.dataDir, customReposFileName))
	if err == nil {
		_ = json.Unmarshal(body, &file)
	}
	repos := make([]Repo, 0, len(file.Repos))
	for _, repo := range file.Repos {
		repo.BuiltIn = false
		if repo.ID != "" && repo.Owner != "" && repo.Name != "" {
			repos = append(repos, repo)
		}
	}
	return repos
}

func (manager *Manager) saveCustomRepos(repos []Repo) error {
	return writeJSONFile(filepath.Join(manager.dataDir, customReposFileName), customReposFile{Repos: repos})
}

func writeJSONFile(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}

// sanitizeSkillDirName 把 skill 名称清洗成安全的目录名。
func sanitizeSkillDirName(name string) string {
	replacer := strings.NewReplacer(
		"<", "-", ">", "-", ":", "-", "\"", "-", "/", "-",
		"\\", "-", "|", "-", "?", "-", "*", "-", "\n", " ", "\r", " ", "\t", " ",
	)
	cleaned := strings.TrimSpace(replacer.Replace(name))
	cleaned = strings.Trim(cleaned, ". ")
	return cleaned
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// lastPathSegment 取斜杠路径的最后一段；空路径返回空而不是 "."。
func lastPathSegment(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

type skillMetadata struct {
	Name        string
	Description string
}

// parseSkillMarkdown 从 SKILL.md 提取 frontmatter 的 name 与 description。
// 解析是宽松的：支持同行取值与缩进续行（含 >-、| 块标量）；
// frontmatter 缺失或字段为空时，回退取正文第一个非空段落截断作为描述。
func parseSkillMarkdown(content string) skillMetadata {
	meta := skillMetadata{}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	bodyStart := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		end := -1
		for index := 1; index < len(lines); index++ {
			if strings.TrimSpace(lines[index]) == "---" {
				end = index
				break
			}
		}
		if end > 0 {
			meta.Name = extractFrontmatterField(lines[1:end], "name")
			meta.Description = extractFrontmatterField(lines[1:end], "description")
			bodyStart = end + 1
		}
	}
	if meta.Description == "" {
		meta.Description = firstBodyParagraph(lines[bodyStart:])
	}
	const descriptionLimit = 300
	if runes := []rune(meta.Description); len(runes) > descriptionLimit {
		meta.Description = string(runes[:descriptionLimit]) + "…"
	}
	return meta
}

func extractFrontmatterField(lines []string, field string) string {
	prefix := field + ":"
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		isBlockScalar := value == "|" || value == ">" || value == "|-" || value == ">-"
		if isBlockScalar {
			value = ""
		}
		parts := make([]string, 0, 4)
		if value != "" {
			parts = append(parts, strings.Trim(value, `"'`))
		}
		// 收集缩进续行（块标量内容或折叠续行）。
		for next := index + 1; next < len(lines); next++ {
			line := lines[next]
			if strings.TrimSpace(line) == "" {
				if isBlockScalar {
					parts = append(parts, "")
					continue
				}
				break
			}
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				break
			}
			parts = append(parts, strings.TrimSpace(line))
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

func firstBodyParagraph(lines []string) string {
	paragraph := make([]string, 0, 4)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	return strings.Join(paragraph, " ")
}
