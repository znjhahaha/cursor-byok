// backup.go 提供 skills 目录的 zip 备份与恢复，以及基于来源仓库的更新检测。
package skills

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxRestoreArchiveSize 限制恢复备份 zip 的体积。
const maxRestoreArchiveSize = 500 << 20

// BackupSkills 把安装目录下全部 skill 打包为 zip，返回打包的 skill 数量。
// zip 内路径为 <skill 目录名>/<相对路径>，与恢复逻辑对应。
func (manager *Manager) BackupSkills(zipPath string) (int, error) {
	entries, err := os.ReadDir(manager.installDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("skills 目录不存在，没有可备份的内容")
		}
		return 0, fmt.Errorf("scan skills directory: %w", err)
	}
	skillDirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && !isTransientSkillDir(entry.Name()) {
			skillDirs = append(skillDirs, entry.Name())
		}
	}
	if len(skillDirs) == 0 {
		return 0, fmt.Errorf("没有可备份的 skill")
	}
	sort.Strings(skillDirs)

	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return 0, fmt.Errorf("create backup directory: %w", err)
	}
	file, err := os.Create(zipPath)
	if err != nil {
		return 0, fmt.Errorf("create backup file: %w", err)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			// 失败时清掉半截 zip，避免留下一个看似可用的损坏备份。
			_ = os.Remove(zipPath)
		}
	}()
	writer := zip.NewWriter(file)
	for _, dirName := range skillDirs {
		if err := addDirToZip(writer, filepath.Join(manager.installDir, dirName), dirName); err != nil {
			_ = writer.Close()
			_ = file.Close()
			return 0, err
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		return 0, fmt.Errorf("finish backup zip: %w", err)
	}
	if err := file.Close(); err != nil {
		return 0, fmt.Errorf("close backup file: %w", err)
	}
	succeeded = true
	return len(skillDirs), nil
}

func addDirToZip(writer *zip.Writer, sourceDir string, zipPrefix string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		zipName := zipPrefix + "/" + filepath.ToSlash(relPath)
		header := &zip.FileHeader{Name: zipName, Method: zip.Deflate}
		if info, err := entry.Info(); err == nil {
			header.Modified = info.ModTime()
		}
		target, err := writer.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create zip entry %q: %w", zipName, err)
		}
		source, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %q: %w", path, err)
		}
		_, copyErr := io.Copy(target, source)
		closeErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("write zip entry %q: %w", zipName, copyErr)
		}
		return closeErr
	})
}

// RestoreSkills 从备份 zip 恢复 skill，按顶层目录逐个走备份式替换（同名整目录覆盖），
// 返回恢复的 skill 数量。zip 中不属于任何顶层目录的文件被忽略。
func (manager *Manager) RestoreSkills(zipPath string) (int, error) {
	info, err := os.Stat(zipPath)
	if err != nil {
		return 0, fmt.Errorf("读取备份文件失败: %w", err)
	}
	if info.Size() > maxRestoreArchiveSize {
		return 0, fmt.Errorf("备份文件超过 %dMB 上限", maxRestoreArchiveSize>>20)
	}
	body, err := os.ReadFile(zipPath)
	if err != nil {
		return 0, fmt.Errorf("读取备份文件失败: %w", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return 0, fmt.Errorf("备份文件不是有效的 zip: %w", err)
	}

	groups := make(map[string][]*zip.File)
	for _, file := range reader.File {
		name := strings.Trim(file.Name, "/")
		if name == "" || strings.HasSuffix(file.Name, "/") {
			continue
		}
		topDir, rest, ok := strings.Cut(name, "/")
		if !ok || rest == "" || isTransientSkillDir(topDir) {
			continue
		}
		groups[topDir] = append(groups[topDir], file)
	}
	if len(groups) == 0 {
		return 0, fmt.Errorf("备份中没有发现 skill 目录")
	}

	if err := os.MkdirAll(manager.installDir, 0o755); err != nil {
		return 0, fmt.Errorf("create skills directory: %w", err)
	}
	dirNames := make([]string, 0, len(groups))
	for dirName := range groups {
		dirNames = append(dirNames, dirName)
	}
	sort.Strings(dirNames)

	restored := 0
	for _, dirName := range dirNames {
		cleanName := sanitizeSkillDirName(dirName)
		if cleanName == "" {
			continue
		}
		targetDir := filepath.Join(manager.installDir, cleanName)
		stagingDir := targetDir + installStagingSuffix
		_ = os.RemoveAll(stagingDir)
		if err := extractZipGroup(groups[dirName], dirName, stagingDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return restored, fmt.Errorf("恢复 %q 失败: %w", dirName, err)
		}
		if err := replaceDirWithStaging(targetDir, stagingDir); err != nil {
			return restored, fmt.Errorf("恢复 %q 失败: %w", dirName, err)
		}
		restored++
	}
	return restored, nil
}

func extractZipGroup(files []*zip.File, topDir string, stagingDir string) error {
	prefix := topDir + "/"
	for _, file := range files {
		relPath := strings.TrimPrefix(strings.Trim(file.Name, "/"), prefix)
		if relPath == "" {
			continue
		}
		destPath, err := secureExtractPath(stagingDir, relPath)
		if err != nil {
			return err
		}
		if err := osMkdirAllForFile(destPath); err != nil {
			return err
		}
		body, err := readZipFile(file, maxExtractFileSize)
		if err != nil {
			return fmt.Errorf("read backup entry %q: %w", file.Name, err)
		}
		if err := osWriteFile(destPath, body); err != nil {
			return err
		}
	}
	return nil
}

// SkillUpdateStatus 是单个已安装 skill 的更新检测结果。
type SkillUpdateStatus struct {
	DirName         string `json:"dirName"`
	Name            string `json:"name"`
	SourceRepo      string `json:"sourceRepo"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Error           string `json:"error,omitempty"`
}

// CheckSkillUpdates 对所有带来源记录的已安装 skill 做更新检测：
// 按来源仓库分组下载归档（优先走缓存），比对远端与本地 SKILL.md 内容。
// 手工放入（无来源记录）的 skill 无从对照，不出现在结果里。
func (manager *Manager) CheckSkillUpdates() ([]SkillUpdateStatus, error) {
	records := manager.loadInstalledRecords()
	if len(records) == 0 {
		return []SkillUpdateStatus{}, nil
	}
	dirNames := make([]string, 0, len(records))
	for dirName := range records {
		dirNames = append(dirNames, dirName)
	}
	sort.Strings(dirNames)

	archives := make(map[string]*repoArchive)
	statuses := make([]SkillUpdateStatus, 0, len(dirNames))
	for _, dirName := range dirNames {
		record := records[dirName]
		localPath := filepath.Join(manager.installDir, dirName, "SKILL.md")
		localBody, err := os.ReadFile(localPath)
		if err != nil {
			// 目录已被手工删除：跳过，不算错误。
			continue
		}
		status := SkillUpdateStatus{DirName: dirName, Name: dirName, SourceRepo: record.Repo}
		archive, cached := archives[record.Repo]
		if !cached {
			// 失败也写入 nil 占位，同一仓库的其余 skill 不再重复尝试下载。
			archive = nil
			if repo, err := manager.findRepo(record.Repo); err == nil {
				archive, _ = manager.repoArchive(repo, false)
			}
			archives[record.Repo] = archive
		}
		if archive == nil {
			status.Error = fmt.Sprintf("仓库 %s 不可用", record.Repo)
			statuses = append(statuses, status)
			continue
		}
		remoteBody, err := archive.readFile(joinArchivePath(record.Subdir, "SKILL.md"))
		if err != nil {
			status.Error = "远端已不存在该 skill"
			statuses = append(statuses, status)
			continue
		}
		status.UpdateAvailable = !skillContentEqual(localBody, remoteBody)
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// skillContentEqual 比较 SKILL.md 内容；换行符差异（用户本地编辑器可能改写 CRLF）不算变更。
func skillContentEqual(left []byte, right []byte) bool {
	normalize := func(body []byte) []byte {
		return bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	}
	return bytes.Equal(normalize(left), normalize(right))
}
