package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	coreExecutableName = "sing-box.exe"
	maxCoreDownload    = 200 << 20
	maxCoreBinary      = 100 << 20
)

var (
	coreTagPattern     = regexp.MustCompile(`(?i)^v?(\d+\.\d+\.\d+(?:-[0-9a-z]+(?:[.-][0-9a-z]+)*)?)$`)
	testChannelPattern = regexp.MustCompile(`(?i)(?:^|[-._])(alpha|beta|rc|dev|nightly|preview)(?:[-._]|\d|$)`)
)

type CoreConfig struct {
	URLTemplate      string `json:"urlTemplate"`
	Version          string `json:"version"`
	Channel          string `json:"channel"`
	CorePath         string `json:"corePath"`
	InstalledVersion string `json:"installedVersion"`
	Installed        bool   `json:"installed"`
	LatestVersion    string `json:"latestVersion"`
	UpdateAvailable  bool   `json:"updateAvailable"`
}

type CoreService struct {
	executableDir string
	mu            sync.Mutex
}

func NewCoreService() (*CoreService, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	return &CoreService{executableDir: filepath.Dir(executable)}, nil
}

func (s *CoreService) ServiceStartup(context.Context, application.ServiceOptions) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	s.executableDir = filepath.Dir(executable)
	return nil
}

func (s *CoreService) ServiceName() string {
	return "CoreService"
}

func (s *CoreService) GetConfig() (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadConfigLocked()
}

func (s *CoreService) SaveURL(rawURL string) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := parseCoreURL(rawURL)
	if err != nil {
		return CoreConfig{}, err
	}
	config.CorePath = s.corePath()
	config.InstalledVersion = ""
	config.Installed = fileExists(config.CorePath)
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	return config, nil
}

func (s *CoreService) DownloadCore() (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	if config.URLTemplate == "" || config.Version == "" {
		return CoreConfig{}, errors.New("core download URL has not been configured")
	}

	targetVersion := config.Version
	if config.UpdateAvailable && config.LatestVersion != "" {
		targetVersion = config.LatestVersion
	}
	downloadURL := strings.ReplaceAll(config.URLTemplate, "{version}", targetVersion)
	parsedURL, err := url.Parse(downloadURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return CoreConfig{}, errors.New("core download URL is invalid")
	}

	archivePath, err := s.download(downloadURL)
	if err != nil {
		return CoreConfig{}, err
	}
	defer os.Remove(archivePath)

	corePath := s.corePath()
	if err := s.extractCore(archivePath, corePath); err != nil {
		return CoreConfig{}, err
	}

	config.CorePath = corePath
	config.Version = targetVersion
	config.InstalledVersion = targetVersion
	config.Installed = true
	config.LatestVersion = targetVersion
	config.UpdateAvailable = false
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	return config, nil
}

func (s *CoreService) CheckUpdate() (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	if config.URLTemplate == "" || config.Version == "" {
		return CoreConfig{}, errors.New("core download URL has not been configured")
	}

	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil {
		return CoreConfig{}, err
	}
	latest, err := findLatestTag(owner, repository, config.Channel)
	if err != nil {
		return CoreConfig{}, err
	}
	current, err := parseCoreVersionParts(config.Version)
	if err != nil {
		return CoreConfig{}, fmt.Errorf("current core version is invalid: %w", err)
	}

	config.LatestVersion = latest
	config.UpdateAvailable = compareCoreVersions(mustParseCoreVersion(latest), current) > 0
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	return config, nil
}

func (s *CoreService) loadConfigLocked() (CoreConfig, error) {
	config := CoreConfig{CorePath: s.corePath()}
	data, err := os.ReadFile(s.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return CoreConfig{}, fmt.Errorf("read core config: %w", err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return CoreConfig{}, fmt.Errorf("parse core config: %w", err)
	}
	config.CorePath = s.corePath()
	config.Installed = fileExists(config.CorePath)
	return config, nil
}

func (s *CoreService) saveConfigLocked(config CoreConfig) error {
	config.CorePath = s.corePath()
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomically(s.configPath(), data, 0o600)
}

func (s *CoreService) download(downloadURL string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Minute}
	response, err := client.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download core: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download core: server returned %s", response.Status)
	}
	if response.ContentLength > maxCoreDownload {
		return "", errors.New("core archive is too large")
	}

	temporary, err := os.CreateTemp(s.executableDir, ".core-download-*.zip")
	if err != nil {
		return "", fmt.Errorf("create core archive: %w", err)
	}
	path := temporary.Name()
	defer func() {
		if temporary != nil {
			temporary.Close()
		}
	}()

	written, err := io.Copy(temporary, io.LimitReader(response.Body, maxCoreDownload+1))
	if err != nil {
		temporary.Close()
		os.Remove(path)
		return "", fmt.Errorf("save core archive: %w", err)
	}
	if written > maxCoreDownload {
		temporary.Close()
		os.Remove(path)
		return "", errors.New("core archive is too large")
	}
	if err := temporary.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close core archive: %w", err)
	}
	temporary = nil
	return path, nil
}

func (s *CoreService) extractCore(archivePath, targetPath string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open core archive: %w", err)
	}
	defer archive.Close()

	var selected *zip.File
	for _, entry := range archive.File {
		name := filepath.Base(strings.ReplaceAll(entry.Name, "\\", "/"))
		if name != coreExecutableName && name != "sing-box" {
			continue
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.UncompressedSize64 > maxCoreBinary {
			continue
		}
		if selected == nil || strings.Count(entry.Name, "/") < strings.Count(selected.Name, "/") {
			selected = entry
		}
	}
	if selected == nil {
		return errors.New("sing-box executable was not found in the core archive")
	}

	reader, err := selected.Open()
	if err != nil {
		return fmt.Errorf("read core executable: %w", err)
	}
	defer reader.Close()

	temporary, err := os.CreateTemp(s.executableDir, ".sing-box-*.exe")
	if err != nil {
		return fmt.Errorf("create core file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, err := io.Copy(temporary, io.LimitReader(reader, maxCoreBinary+1))
	if err != nil {
		temporary.Close()
		return fmt.Errorf("extract core executable: %w", err)
	}
	if written == 0 || written > maxCoreBinary {
		temporary.Close()
		return errors.New("core executable is invalid or too large")
	}
	if err := temporary.Chmod(0o755); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	backupPath := targetPath + ".old"
	_ = os.Remove(backupPath)
	if err := os.Rename(targetPath, backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prepare core replacement: %w", err)
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		_ = os.Rename(backupPath, targetPath)
		return fmt.Errorf("replace core executable: %w", err)
	}
	_ = os.Remove(backupPath)
	return nil
}

func parseCoreURL(rawURL string) (CoreConfig, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return CoreConfig{}, errors.New("请输入有效的 HTTP(S) 核心下载地址")
	}

	segments := pathSegments(parsedURL.Path)
	if len(segments) < 5 || !strings.EqualFold(segments[2], "releases") || !strings.EqualFold(segments[3], "download") {
		return CoreConfig{}, errors.New("请输入 GitHub Release 的核心 ZIP 下载地址")
	}

	tag := segments[4]
	version, err := parseCoreVersion(tag)
	if err != nil {
		return CoreConfig{}, errors.New("无法从地址识别版本号")
	}

	channel := coreChannel(version)
	parsedURL.Path = strings.ReplaceAll(parsedURL.Path, version, "{version}")
	parsedURL.RawPath = ""
	templateURL := parsedURL.String()
	templateURL = strings.ReplaceAll(templateURL, "%7Bversion%7D", "{version}")
	templateURL = strings.ReplaceAll(templateURL, "%7bversion%7d", "{version}")

	return CoreConfig{
		URLTemplate: templateURL,
		Version:     version,
		Channel:     channel,
	}, nil
}

func pathSegments(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			segments = append(segments, part)
		}
	}
	return segments
}

func parseCoreVersion(value string) (string, error) {
	value, err := url.PathUnescape(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	match := coreTagPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return "", fmt.Errorf("unsupported version %q", value)
	}
	return match[1], nil
}

func coreChannel(version string) string {
	if testChannelPattern.MatchString(version) {
		return "test"
	}
	return "stable"
}

func githubRepository(template string) (string, string, error) {
	parsedURL, err := url.Parse(template)
	if err != nil || !strings.EqualFold(parsedURL.Hostname(), "github.com") {
		return "", "", errors.New("核心地址必须来自 github.com")
	}
	segments := pathSegments(parsedURL.Path)
	if len(segments) < 5 || !strings.EqualFold(segments[2], "releases") || !strings.EqualFold(segments[3], "download") || !strings.Contains(segments[4], "{version}") {
		return "", "", errors.New("核心地址不是有效的 GitHub Release 通用地址")
	}
	if segments[0] == "" || segments[1] == "" {
		return "", "", errors.New("无法识别 GitHub 仓库")
	}
	return segments[0], segments[1], nil
}

type githubTag struct {
	Name string `json:"name"`
}

func findLatestTag(owner, repository, channel string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var latest string
	var latestVersion coreVersion
	for page := 1; page <= 100; page++ {
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/tags?per_page=100&page=%d", url.PathEscape(owner), url.PathEscape(repository), page)
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return "", fmt.Errorf("check core update: %w", err)
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "sing-box-gui")

		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("check core update: %w", err)
		}
		var tags []githubTag
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&tags)
		closeErr := response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return "", fmt.Errorf("check core update: GitHub returned %s", response.Status)
		}
		if decodeErr != nil {
			return "", fmt.Errorf("parse GitHub tags: %w", decodeErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close GitHub response: %w", closeErr)
		}

		for _, tag := range tags {
			version, err := parseCoreVersion(tag.Name)
			if err != nil || coreChannel(version) != channel {
				continue
			}
			parsed, err := parseCoreVersionParts(version)
			if err != nil {
				continue
			}
			if latest == "" || compareCoreVersions(parsed, latestVersion) > 0 {
				latest = version
				latestVersion = parsed
			}
		}
		if len(tags) < 100 {
			break
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no %s core tags found", channel)
	}
	return latest, nil
}

type coreVersion struct {
	major  int
	minor  int
	patch  int
	suffix []string
}

func parseCoreVersionParts(value string) (coreVersion, error) {
	version, err := parseCoreVersion(value)
	if err != nil {
		return coreVersion{}, err
	}
	base := version
	suffix := []string(nil)
	if index := strings.IndexByte(version, '-'); index >= 0 {
		base = version[:index]
		for _, part := range strings.FieldsFunc(version[index+1:], func(r rune) bool { return r == '.' || r == '-' }) {
			if part != "" {
				suffix = append(suffix, part)
			}
		}
	}
	var parsed coreVersion
	if _, err := fmt.Sscanf(base, "%d.%d.%d", &parsed.major, &parsed.minor, &parsed.patch); err != nil {
		return coreVersion{}, fmt.Errorf("unsupported version %q", value)
	}
	parsed.suffix = suffix
	return parsed, nil
}

func mustParseCoreVersion(value string) coreVersion {
	parsed, _ := parseCoreVersionParts(value)
	return parsed
}

func compareCoreVersions(left, right coreVersion) int {
	for _, pair := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.suffix) == 0 && len(right.suffix) > 0 {
		return 1
	}
	if len(left.suffix) > 0 && len(right.suffix) == 0 {
		return -1
	}
	for index := 0; index < len(left.suffix) && index < len(right.suffix); index++ {
		leftPart, rightPart := left.suffix[index], right.suffix[index]
		leftNumber, leftErr := strconv.Atoi(leftPart)
		rightNumber, rightErr := strconv.Atoi(rightPart)
		if leftErr == nil && rightErr == nil {
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
			continue
		}
		if leftErr == nil && rightErr != nil {
			return -1
		}
		if leftErr != nil && rightErr == nil {
			return 1
		}
		if strings.ToLower(leftPart) < strings.ToLower(rightPart) {
			return -1
		}
		if strings.ToLower(leftPart) > strings.ToLower(rightPart) {
			return 1
		}
	}
	if len(left.suffix) < len(right.suffix) {
		return -1
	}
	if len(left.suffix) > len(right.suffix) {
		return 1
	}
	return 0
}

func (s *CoreService) configPath() string {
	return filepath.Join(s.executableDir, "core.json")
}

func (s *CoreService) corePath() string {
	return filepath.Join(s.executableDir, coreExecutableName)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".core-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
