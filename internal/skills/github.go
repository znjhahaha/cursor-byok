// github.go 负责从 GitHub 下载仓库归档并按需读取其中的 skill 文件。
// 走 codeload zip：单个 HTTP 请求拿到整个仓库，不消耗 GitHub API 配额。
package skills

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// maxArchiveSize 限制仓库 zip 体积，防止误加超大仓库拖垮内存。
	maxArchiveSize = 100 << 20
	// maxExtractFileSize 限制单个文件解压体积，防 zip 炸弹。
	maxExtractFileSize = 20 << 20
	// archiveCacheTTL 内缓存的归档直接复用，安装无需重新下载。
	archiveCacheTTL = 10 * time.Minute
)

var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// repoArchive 持有一个已下载仓库 zip 的随机访问视图。
type repoArchive struct {
	reader     *zip.Reader
	rootPrefix string
	fetchedAt  time.Time
}

// parseRepoSpec 解析仓库描述：支持 "owner/repo"、"owner/repo@branch"
// 以及 GitHub URL（https://github.com/owner/repo 或 .../tree/branch）。
func parseRepoSpec(spec string) (Repo, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Repo{}, fmt.Errorf("仓库地址不能为空")
	}
	owner, name, branch := "", "", ""
	if strings.Contains(spec, "github.com") {
		normalized := spec
		if !strings.Contains(normalized, "://") {
			normalized = "https://" + normalized
		}
		parsed, err := url.Parse(normalized)
		if err != nil {
			return Repo{}, fmt.Errorf("无法解析仓库地址: %w", err)
		}
		segments := make([]string, 0, 4)
		for _, segment := range strings.Split(parsed.Path, "/") {
			if segment != "" {
				segments = append(segments, segment)
			}
		}
		if len(segments) < 2 {
			return Repo{}, fmt.Errorf("仓库地址缺少 owner/repo 部分")
		}
		owner = segments[0]
		name = strings.TrimSuffix(segments[1], ".git")
		if len(segments) >= 4 && segments[2] == "tree" {
			branch = segments[3]
		}
	} else {
		body := spec
		if at := strings.LastIndex(body, "@"); at > 0 {
			branch = strings.TrimSpace(body[at+1:])
			body = body[:at]
		}
		parts := strings.Split(strings.Trim(body, "/"), "/")
		if len(parts) != 2 {
			return Repo{}, fmt.Errorf("仓库格式应为 owner/repo 或 owner/repo@branch")
		}
		owner, name = parts[0], strings.TrimSuffix(parts[1], ".git")
	}
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if !repoNamePattern.MatchString(owner) || !repoNamePattern.MatchString(name) {
		return Repo{}, fmt.Errorf("仓库 owner 或名称含非法字符")
	}
	repo := Repo{Owner: owner, Name: name, Branch: strings.TrimSpace(branch)}
	repo.ID = repo.Owner + "/" + repo.Name
	if repo.Branch != "" {
		repo.ID += "@" + repo.Branch
	}
	return repo, nil
}

// repoArchive 返回仓库归档，优先复用 TTL 内的缓存。
func (manager *Manager) repoArchive(repo Repo, refresh bool) (*repoArchive, error) {
	manager.mu.Lock()
	cached := manager.archives[repo.ID]
	manager.mu.Unlock()
	if cached != nil && !refresh && time.Since(cached.fetchedAt) < archiveCacheTTL {
		return cached, nil
	}
	archive, err := manager.downloadArchive(repo)
	if err != nil {
		// 下载失败时回退到过期缓存，弱网下安装仍可用刚浏览过的归档完成。
		if cached != nil {
			return cached, nil
		}
		return nil, err
	}
	manager.mu.Lock()
	manager.archives[repo.ID] = archive
	manager.mu.Unlock()
	return archive, nil
}

func (manager *Manager) downloadArchive(repo Repo) (*repoArchive, error) {
	ref := "HEAD"
	if repo.Branch != "" {
		ref = "refs/heads/" + repo.Branch
	}
	archiveURL := fmt.Sprintf("https://codeload.github.com/%s/%s/zip/%s", repo.Owner, repo.Name, ref)
	response, err := manager.httpClient.Get(archiveURL)
	if err != nil {
		return nil, fmt.Errorf("下载仓库失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return nil, fmt.Errorf("仓库 %s 不存在或分支无效（私有仓库不支持）", repo.ID)
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("下载仓库失败: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取仓库归档失败: %w", err)
	}
	if len(body) > maxArchiveSize {
		return nil, fmt.Errorf("仓库归档超过 %dMB 上限", maxArchiveSize>>20)
	}
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("解析仓库归档失败: %w", err)
	}
	rootPrefix := ""
	if len(reader.File) > 0 {
		first := reader.File[0].Name
		if index := strings.Index(first, "/"); index >= 0 {
			rootPrefix = first[:index+1]
		}
	}
	return &repoArchive{reader: reader, rootPrefix: rootPrefix, fetchedAt: time.Now()}, nil
}

// scanSkills 找出归档中所有 SKILL.md 并解析元数据。
func (archive *repoArchive) scanSkills() []RemoteSkill {
	skills := make([]RemoteSkill, 0, 16)
	for _, file := range archive.reader.File {
		name := strings.TrimPrefix(file.Name, archive.rootPrefix)
		if name != "SKILL.md" && !strings.HasSuffix(name, "/SKILL.md") {
			continue
		}
		subdir := strings.TrimSuffix(strings.TrimSuffix(name, "SKILL.md"), "/")
		body, err := readZipFile(file, 1<<20)
		if err != nil {
			continue
		}
		meta := parseSkillMarkdown(string(body))
		skills = append(skills, RemoteSkill{
			Name:        firstNonEmptyString(meta.Name, lastPathSegment(subdir)),
			Description: meta.Description,
			Subdir:      subdir,
		})
	}
	sort.Slice(skills, func(left, right int) bool {
		return skills[left].Subdir < skills[right].Subdir
	})
	return skills
}

// readFile 读取归档内相对路径（不含顶层目录）对应的文件。
func (archive *repoArchive) readFile(relPath string) ([]byte, error) {
	relPath = strings.Trim(path.Clean("/"+relPath), "/")
	target := archive.rootPrefix + relPath
	for _, file := range archive.reader.File {
		if file.Name == target {
			return readZipFile(file, maxExtractFileSize)
		}
	}
	return nil, fmt.Errorf("archive entry %q not found", relPath)
}

// extractDir 把归档内 subdir 下的全部文件解压到 targetDir。
// subdir 为空表示解压整个仓库（单 skill 仓库场景）。
func (archive *repoArchive) extractDir(subdir string, targetDir string) error {
	prefix := archive.rootPrefix
	if subdir != "" {
		prefix += subdir + "/"
	}
	extracted := 0
	for _, file := range archive.reader.File {
		if !strings.HasPrefix(file.Name, prefix) {
			continue
		}
		relPath := strings.TrimPrefix(file.Name, prefix)
		if relPath == "" || strings.HasSuffix(file.Name, "/") {
			continue
		}
		destPath, err := secureExtractPath(targetDir, relPath)
		if err != nil {
			return err
		}
		if err := osMkdirAllForFile(destPath); err != nil {
			return err
		}
		body, err := readZipFile(file, maxExtractFileSize)
		if err != nil {
			return fmt.Errorf("read archive entry %q: %w", file.Name, err)
		}
		if err := osWriteFile(destPath, body); err != nil {
			return err
		}
		extracted++
	}
	if extracted == 0 {
		return fmt.Errorf("目录 %q 在仓库中不存在或为空", subdir)
	}
	return nil
}

// joinArchivePath 拼接归档内相对路径；subdir 为空时直接返回文件名。
func joinArchivePath(subdir string, fileName string) string {
	if subdir == "" {
		return fileName
	}
	return subdir + "/" + fileName
}

// secureExtractPath 把归档内相对路径映射到目标目录内的绝对路径，拒绝路径穿越（zip slip）。
func secureExtractPath(targetDir string, relPath string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(relPath))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("归档条目路径非法: %q", relPath)
	}
	return filepath.Join(targetDir, cleaned), nil
}

func osMkdirAllForFile(filePath string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	return nil
}

func osWriteFile(filePath string, body []byte) error {
	if err := os.WriteFile(filePath, body, 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func readZipFile(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("entry exceeds %d bytes", limit)
	}
	return body, nil
}
