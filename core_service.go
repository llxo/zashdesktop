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
	"os/exec"
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
	coreTagPattern     = regexp.MustCompile(`(?i)^v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)$`)
	coreOutputPattern  = regexp.MustCompile(`(?i)(^|[^0-9a-z])v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)([^0-9a-z]|$)`)
	testChannelPattern = regexp.MustCompile(`(?i)(^|[-._])(alpha|beta|rc|dev|nightly|preview)([-._]|\d|$)`)
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
	PendingVersion   string `json:"pendingVersion"`
	UpdatePending    bool   `json:"updatePending"`
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

	existing, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	config, err := parseCoreURL(rawURL)
	if err != nil {
		return CoreConfig{}, err
	}
	config.CorePath = s.corePath()
	config.Version = existing.Version
	config.Channel = existing.Channel
	config.InstalledVersion = existing.InstalledVersion
	config.Installed = fileExists(config.CorePath)
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	return config, nil
}

func (s *CoreService) DownloadCore(currentVersion string) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	s.applyCurrentVersion(&config, currentVersion)
	if config.URLTemplate == "" {
		return CoreConfig{}, errors.New("core download URL has not been configured")
	}

	targetVersion := config.LatestVersion
	if targetVersion == "" {
		targetVersion, err = findLatestTagForConfig(config)
		if err != nil {
			return CoreConfig{}, err
		}
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
	installed, err := s.extractCore(archivePath, corePath)
	if err != nil {
		return CoreConfig{}, err
	}

	config.CorePath = corePath
	if installed {
		installedVersion, versionErr := readCoreVersion(corePath)
		if versionErr != nil {
			return CoreConfig{}, versionErr
		}
		config.Version = installedVersion
		config.Channel = coreChannel(installedVersion)
		config.InstalledVersion = installedVersion
		config.Installed = true
		config.LatestVersion = targetVersion
		config.UpdateAvailable = compareCoreVersions(mustParseCoreVersion(targetVersion), mustParseCoreVersion(installedVersion)) > 0
		config.PendingVersion = ""
		config.UpdatePending = false
	} else {
		pendingVersion, versionErr := readCoreVersion(s.pendingCorePath())
		if versionErr != nil {
			return CoreConfig{}, versionErr
		}
		config.LatestVersion = targetVersion
		config.PendingVersion = pendingVersion
		config.UpdateAvailable = true
		config.UpdatePending = true
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	return config, nil
}

func (s *CoreService) CheckUpdate(currentVersion string) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	s.applyCurrentVersion(&config, currentVersion)
	if config.URLTemplate == "" {
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
	config.LatestVersion = latest
	if config.Version == "" {
		config.UpdateAvailable = true
	} else {
		config.UpdateAvailable = compareCoreVersions(mustParseCoreVersion(latest), mustParseCoreVersion(config.Version)) > 0
	}
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
	// The running core is authoritative; persisted version fields are only legacy metadata.
	config.Version = ""
	config.InstalledVersion = ""
	config.Channel = ""
	s.applyCurrentVersion(&config, "")
	if config.UpdatePending && config.PendingVersion != "" && fileExists(s.pendingCorePath()) {
		installed, err := s.installPendingCore()
		if err != nil {
			return CoreConfig{}, err
		}
		if installed {
			installedVersion, versionErr := readCoreVersion(s.corePath())
			if versionErr != nil {
				return CoreConfig{}, versionErr
			}
			config.Version = installedVersion
			config.InstalledVersion = installedVersion
			config.Channel = coreChannel(installedVersion)
			config.UpdateAvailable = config.LatestVersion != "" && compareCoreVersions(mustParseCoreVersion(config.LatestVersion), mustParseCoreVersion(installedVersion)) > 0
			config.PendingVersion = ""
			config.UpdatePending = false
			config.Installed = true
			if err := s.saveConfigLocked(config); err != nil {
				return CoreConfig{}, err
			}
		}
	}
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

func newCoreHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = systemProxy
	return &http.Client{Timeout: timeout, Transport: transport}
}

func (s *CoreService) download(downloadURL string) (string, error) {
	client := newCoreHTTPClient(20 * time.Minute)
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

func (s *CoreService) extractCore(archivePath, targetPath string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return false, fmt.Errorf("create core directory: %w", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return false, fmt.Errorf("open core archive: %w", err)
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
		return false, errors.New("sing-box executable was not found in the core archive")
	}

	reader, err := selected.Open()
	if err != nil {
		return false, fmt.Errorf("read core executable: %w", err)
	}
	defer reader.Close()

	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".sing-box-*.exe")
	if err != nil {
		return false, fmt.Errorf("create core file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, err := io.Copy(temporary, io.LimitReader(reader, maxCoreBinary+1))
	if err != nil {
		temporary.Close()
		return false, fmt.Errorf("extract core executable: %w", err)
	}
	if written == 0 || written > maxCoreBinary {
		temporary.Close()
		return false, errors.New("core executable is invalid or too large")
	}
	if err := temporary.Chmod(0o755); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}

	installed, err := s.replaceCoreExecutable(temporaryPath, targetPath)
	if err != nil {
		return false, err
	}
	if installed {
		return true, nil
	}

	pendingPath := s.pendingCorePath()
	_ = os.Remove(pendingPath)
	if err := os.Rename(temporaryPath, pendingPath); err != nil {
		return false, fmt.Errorf("save pending core replacement: %w", err)
	}
	return false, nil
}

func (s *CoreService) installPendingCore() (bool, error) {
	return s.replaceCoreExecutable(s.pendingCorePath(), s.corePath())
}

func (s *CoreService) replaceCoreExecutable(sourcePath, targetPath string) (bool, error) {
	backupPath := targetPath + ".old"
	_ = os.Remove(backupPath)
	if err := os.Rename(targetPath, backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		if isFileLockedError(err) {
			return false, nil
		}
		return false, fmt.Errorf("prepare core replacement: %w", err)
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		_ = os.Rename(backupPath, targetPath)
		return false, fmt.Errorf("replace core executable: %w", err)
	}
	_ = os.Remove(backupPath)
	return true, nil
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
	channel := ""
	if !strings.Contains(tag, "{version}") {
		channel = coreChannel(tag)
		parsedURL.Path = strings.ReplaceAll(parsedURL.Path, tag, "{version}")
		tagVersion := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
		if tagVersion != tag {
			parsedURL.Path = strings.ReplaceAll(parsedURL.Path, tagVersion, "{version}")
		}
	}
	parsedURL.RawPath = ""
	templateURL := parsedURL.String()
	templateURL = strings.ReplaceAll(templateURL, "%7Bversion%7D", "{version}")
	templateURL = strings.ReplaceAll(templateURL, "%7bversion%7d", "{version}")

	return CoreConfig{
		URLTemplate: templateURL,
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
	if len(match) < 2 {
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
	client := newCoreHTTPClient(30 * time.Second)
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
			if err != nil || (channel != "" && coreChannel(version) != channel) {
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

func findLatestTagForConfig(config CoreConfig) (string, error) {
	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil {
		return "", err
	}
	return findLatestTag(owner, repository, config.Channel)
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

func (s *CoreService) coreDir() string {
	return filepath.Join(s.executableDir, "sing-box")
}

func (s *CoreService) corePath() string {
	return filepath.Join(s.coreDir(), coreExecutableName)
}

func (s *CoreService) pendingCorePath() string {
	return filepath.Join(s.coreDir(), ".sing-box.pending.exe")
}

func (s *CoreService) applyCurrentVersion(config *CoreConfig, supplied string) {
	version := normalizeCoreVersion(supplied)
	if version == "" && fileExists(s.corePath()) {
		version, _ = readCoreVersion(s.corePath())
	}
	if version == "" {
		return
	}
	config.Version = version
	config.Channel = coreChannel(version)
	if fileExists(s.corePath()) {
		config.InstalledVersion = version
		config.Installed = true
	}
}

func normalizeCoreVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if version, err := parseCoreVersion(value); err == nil {
		return version
	}
	match := coreOutputPattern.FindStringSubmatch(value)
	if len(match) != 6 {
		return ""
	}
	return match[2]
}

func readCoreVersion(path string) (string, error) {
	if !fileExists(path) {
		return "", errors.New("sing-box core is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read sing-box core version: %w", err)
	}
	version := normalizeCoreVersion(string(output))
	if version == "" {
		return "", errors.New("unable to read sing-box core version")
	}
	return version, nil
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
