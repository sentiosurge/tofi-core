package skills

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// SourceType 表示技能来源类型
type SourceType string

const (
	SourceGitHub SourceType = "github"
	SourceGitLab SourceType = "gitlab"
	SourceGit    SourceType = "git"
	SourceLocal  SourceType = "local"
)

// ParsedSource 解析后的技能来源
// 兼容 skills CLI 的 owner/repo@skill 格式
type ParsedSource struct {
	Type        SourceType // github, gitlab, git, local
	Owner       string     // GitHub/GitLab owner
	Repo        string     // 仓库名
	CloneURL    string     // 完整 git clone URL
	Ref         string     // branch/tag (可选)
	Subpath     string     // repo 内子路径 (可选)
	SkillFilter string     // @skill 过滤器 (可选)
	LocalPath   string     // 本地路径 (source=local 时使用)
}

// DisplayURL 用于显示和存储的 URL
func (p *ParsedSource) DisplayURL() string {
	if p.Type == SourceLocal {
		return p.LocalPath
	}
	if p.Owner != "" && p.Repo != "" {
		s := p.Owner + "/" + p.Repo
		if p.SkillFilter != "" {
			s += "@" + p.SkillFilter
		}
		return s
	}
	return p.CloneURL
}

// shorthandRegex 匹配 owner/repo 或 owner/repo@skill
var shorthandRegex = regexp.MustCompile(`^([a-zA-Z0-9._-]+)/([a-zA-Z0-9._-]+?)(?:@(.+))?$`)

// threeSegmentRegex 匹配 owner/repo/skill (marketplace format)
var threeSegmentRegex = regexp.MustCompile(`^([a-zA-Z0-9._-]+)/([a-zA-Z0-9._-]+)/([a-zA-Z0-9._-]+)$`)

// ParseSource 解析技能来源字符串，兼容 skills CLI 的多种格式
//
// 支持的格式:
//   - owner/repo               → GitHub shorthand
//   - owner/repo@skill         → GitHub shorthand + skill 过滤
//   - https://github.com/o/r   → GitHub URL
//   - https://github.com/o/r/tree/branch/path → GitHub URL + branch + subpath
//   - https://gitlab.com/o/r   → GitLab URL
//   - git@github.com:o/r.git   → Git SSH URL
//   - ./local/path             → 本地路径
//   - /absolute/path           → 本地路径
func ParseSource(input string) (*ParsedSource, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty source")
	}

	// 1. 本地路径
	if isLocalPath(input) {
		absPath, err := filepath.Abs(input)
		if err != nil {
			return nil, fmt.Errorf("invalid local path: %w", err)
		}
		return &ParsedSource{
			Type:      SourceLocal,
			LocalPath: absPath,
		}, nil
	}

	// 2. Git SSH URL: git@github.com:owner/repo.git
	if strings.HasPrefix(input, "git@") {
		return parseGitSSH(input)
	}

	// 3. HTTP(S) URL
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return parseHTTPURL(input)
	}

	// 4a. owner/repo/skill (marketplace 3-segment format → owner/repo@skill)
	if m := threeSegmentRegex.FindStringSubmatch(input); m != nil {
		return &ParsedSource{
			Type:        SourceGitHub,
			Owner:       m[1],
			Repo:        m[2],
			CloneURL:    fmt.Sprintf("https://github.com/%s/%s.git", m[1], m[2]),
			SkillFilter: m[3],
		}, nil
	}

	// 4b. owner/repo[@skill] shorthand
	if m := shorthandRegex.FindStringSubmatch(input); m != nil {
		ps := &ParsedSource{
			Type:     SourceGitHub,
			Owner:    m[1],
			Repo:     m[2],
			CloneURL: fmt.Sprintf("https://github.com/%s/%s.git", m[1], m[2]),
		}
		if m[3] != "" {
			ps.SkillFilter = m[3]
		}
		return ps, nil
	}

	return nil, fmt.Errorf("unsupported source format: %q (use owner/repo, owner/repo@skill, or a Git URL)", input)
}

// isLocalPath 判断是否为本地路径
func isLocalPath(s string) bool {
	return strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "~")
}

// parseGitSSH 解析 git@ SSH URL
func parseGitSSH(input string) (*ParsedSource, error) {
	// git@github.com:owner/repo.git
	input = strings.TrimSuffix(input, ".git")

	parts := strings.SplitN(input, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid git SSH URL: %s", input)
	}

	host := strings.TrimPrefix(parts[0], "git@")
	repoPath := parts[1]
	segments := strings.Split(repoPath, "/")

	if len(segments) < 2 {
		return nil, fmt.Errorf("invalid git SSH URL: missing owner/repo in %s", input)
	}

	sourceType := SourceGit
	if strings.Contains(host, "github.com") {
		sourceType = SourceGitHub
	} else if strings.Contains(host, "gitlab.com") {
		sourceType = SourceGitLab
	}

	return &ParsedSource{
		Type:     sourceType,
		Owner:    segments[0],
		Repo:     segments[1],
		CloneURL: fmt.Sprintf("https://%s/%s/%s.git", host, segments[0], segments[1]),
	}, nil
}

// parseHTTPURL 解析 HTTP(S) URL
func parseHTTPURL(input string) (*ParsedSource, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	host := u.Hostname()
	pathStr := strings.Trim(u.Path, "/")
	pathStr = strings.TrimSuffix(pathStr, ".git")
	segments := strings.Split(pathStr, "/")

	if len(segments) < 2 {
		return nil, fmt.Errorf("URL must contain at least owner/repo: %s", input)
	}

	owner := segments[0]
	repo := segments[1]

	ps := &ParsedSource{
		Owner:    owner,
		Repo:     repo,
		CloneURL: fmt.Sprintf("https://%s/%s/%s.git", host, owner, repo),
	}

	// 确定平台类型
	switch {
	case strings.Contains(host, "github.com"):
		ps.Type = SourceGitHub
	case strings.Contains(host, "gitlab.com"):
		ps.Type = SourceGitLab
	default:
		ps.Type = SourceGit
	}

	// 解析 branch 和 subpath
	// GitHub: /owner/repo/tree/branch/sub/path
	// GitLab: /owner/repo/-/tree/branch/sub/path
	if len(segments) > 2 {
		remaining := segments[2:]

		// GitHub: tree/branch/...
		if remaining[0] == "tree" && len(remaining) >= 2 {
			ps.Ref = remaining[1]
			if len(remaining) > 2 {
				ps.Subpath = strings.Join(remaining[2:], "/")
			}
		}
		// GitLab: -/tree/branch/...
		if remaining[0] == "-" && len(remaining) >= 3 && remaining[1] == "tree" {
			ps.Ref = remaining[2]
			if len(remaining) > 3 {
				ps.Subpath = strings.Join(remaining[3:], "/")
			}
		}
	}

	return ps, nil
}
