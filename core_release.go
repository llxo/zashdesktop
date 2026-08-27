package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	mihomoPrereleaseTag     = "Prerelease-Alpha"
	mihomoTestURLTemplate   = "https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-{version}.zip"
	mihomoStableURLTemplate = "https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-v{version}.zip"
	remoteReleaseCacheTTL   = 30 * time.Minute
)

var (
	coreTagPattern           = regexp.MustCompile(`(?i)^v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)$`)
	coreOutputPattern        = regexp.MustCompile(`(?i)(^|[^0-9a-z])v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)([^0-9a-z]|$)`)
	coreBuildTagPattern      = regexp.MustCompile(`(?i)^v?((?:alpha|beta|rc|dev|nightly|preview)(?:[-._][0-9a-z]+)*)$`)
	coreBuildOutputPattern   = regexp.MustCompile(`(?i)(^|[^0-9a-z])v?((?:alpha|beta|rc|dev|nightly|preview)(?:[-._][0-9a-z]+)*)([^0-9a-z]|$)`)
	coreBuildAssetPattern    = regexp.MustCompile(`(?i)(^|-)((?:alpha|beta|rc|dev|nightly|preview)-[0-9a-z]{7})\.(?:zip|tar\.gz)$`)
	mihomoTestVersionPattern = regexp.MustCompile(`(?i)^alpha-[0-9a-z]{7}$`)
	testChannelPattern       = regexp.MustCompile(`(?i)(^|[-._])(alpha|beta|rc|dev|nightly|preview)([-._]|\d|$)`)
)

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	HTMLURL     string        `json:"html_url"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	Prerelease  bool          `json:"prerelease"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type remoteReleaseCacheItem struct {
	version   string
	fetchedAt time.Time
}

func (s *CoreService) archiveTools() coreArchiveTools {
	return coreArchiveTools{
		baseDir:     s.executableDir,
		maxDownload: maxCoreDownload,
		maxBinary:   maxCoreBinary,
		logf:        coreDebugf,
	}
}

func coreArchiveExecutableMatcher(coreType string) func(string) bool {
	prefix := coreExecutableBaseName
	if normalizedCoreType(coreType) == coreTypeMihomo {
		prefix = mihomoExecutableName
	}
	return func(name string) bool {
		name = strings.ToLower(strings.TrimSuffix(filepath.Base(name), ".exe"))
		return name == prefix || strings.HasPrefix(name, prefix+"-")
	}
}

func coreExecutableNameFor(coreType string) string {
	baseName := coreExecutableBaseName
	if normalizedCoreType(coreType) == coreTypeMihomo {
		baseName = mihomoExecutableName
	}
	return baseName + ".exe"
}

func (s *CoreService) getCachedLatestRelease(owner, repository, channel string) (string, bool) {
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	if s.remoteReleaseCache == nil {
		return "", false
	}
	key := fmt.Sprintf("%s/%s:%s", strings.ToLower(owner), strings.ToLower(repository), strings.ToLower(channel))
	item, ok := s.remoteReleaseCache[key]
	if !ok || time.Since(item.fetchedAt) >= remoteReleaseCacheTTL {
		return "", false
	}
	return item.version, true
}

func (s *CoreService) setCachedLatestRelease(owner, repository, channel, version string) {
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	if s.remoteReleaseCache == nil {
		s.remoteReleaseCache = make(map[string]remoteReleaseCacheItem)
	}
	key := fmt.Sprintf("%s/%s:%s", strings.ToLower(owner), strings.ToLower(repository), strings.ToLower(channel))
	s.remoteReleaseCache[key] = remoteReleaseCacheItem{
		version:   version,
		fetchedAt: time.Now(),
	}
}

func (s *CoreService) clearCachedLatestRelease(owner, repository, channel string) {
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	if s.remoteReleaseCache == nil {
		return
	}
	key := fmt.Sprintf("%s/%s:%s", strings.ToLower(owner), strings.ToLower(repository), strings.ToLower(channel))
	delete(s.remoteReleaseCache, key)
}

func (s *CoreService) CheckUpdate(currentVersion, rawCoreType string) (CoreConfig, error) {
	return s.checkUpdateInternal(currentVersion, rawCoreType, false)
}

func (s *CoreService) ForceCheckUpdate(currentVersion, rawCoreType string) (CoreConfig, error) {
	return s.checkUpdateInternal(currentVersion, rawCoreType, true)
}

func (s *CoreService) checkUpdateInternal(currentVersion, rawCoreType string, force bool) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	s.applyCurrentVersion(&config, currentVersion)
	if config.URLTemplate == "" {
		return CoreConfig{}, errors.New("core download URL has not been configured")
	}

	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil {
		if config.ConfiguredVersion == "" {
			return CoreConfig{}, err
		}
		config.LatestVersion = config.ConfiguredVersion
		config.UpdateAvailable = config.Version == "" || compareCoreVersions(mustParseCoreVersion(config.LatestVersion), mustParseCoreVersion(config.Version)) > 0
		return s.saveCheckedConfig(config, generation)
	}

	var latest string
	if !force {
		if cached, ok := s.getCachedLatestRelease(owner, repository, config.Channel); ok {
			latest = cached
		}
	}
	if latest == "" {
		latest, err = findLatestRelease(owner, repository, config.Channel)
		if err != nil {
			return CoreConfig{}, err
		}
		s.setCachedLatestRelease(owner, repository, config.Channel, latest)
	}

	config.LatestVersion = latest
	if config.Version == "" {
		config.UpdateAvailable = true
	} else {
		config.UpdateAvailable = compareCoreVersions(mustParseCoreVersion(latest), mustParseCoreVersion(config.Version)) > 0
	}
	return s.saveCheckedConfig(config, generation)
}

func (s *CoreService) ValidateURL(rawURL string) (string, error) {
	config, err := parseCoreURL(rawURL)
	if err != nil {
		return "", err
	}
	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil {
		return "", err
	}
	version, err := findLatestRelease(owner, repository, config.Channel)
	if err != nil {
		return "", err
	}
	downloadURL := strings.ReplaceAll(config.URLTemplate, "{version}", version)
	if _, err := findReleaseAssetDigest(owner, repository, version, downloadURL); err != nil {
		return "", err
	}
	return config.URLTemplate, nil
}

func (s *CoreService) DownloadCore(currentVersion, rawCoreType string) (CoreConfig, error) {
	coreDebugf("download request: currentVersion=%q", currentVersion)

	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	var archivePath, targetVersion string
	config, archivePath, targetVersion, err = s.downloadCoreArchive(currentVersion, config)
	if err != nil {
		coreDebugf("download request failed during archive download: err=%v", err)
		return CoreConfig{}, err
	}
	defer os.Remove(archivePath)
	coreDebugf("archive downloaded: type=%s version=%s path=%q", config.CoreType, targetVersion, archivePath)

	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return CoreConfig{}, errors.New("core service is shutting down")
	}
	if s.configGeneration != generation {
		s.mu.Unlock()
		return CoreConfig{}, errors.New("core configuration changed while downloading; please retry")
	}
	s.detectExternalProcessLocked(config.CoreType)
	runningCoreType := ""
	if s.process != nil {
		runningCoreType = normalizedCoreType(s.processCoreType)
	} else if s.externalProcess != nil {
		runningCoreType = normalizedCoreType(s.externalCoreType)
	}
	wasRunning := runningCoreType == config.CoreType
	runArgs := config.RunArgs
	s.mu.Unlock()
	if runningCoreType != "" && !wasRunning {
		coreDebugf("leaving other core running during replacement: runningType=%s targetType=%s", runningCoreType, config.CoreType)
	}

	if wasRunning {
		coreDebugf("stopping core before replacement: type=%s", config.CoreType)
		if err := s.stopCoreProcess(); err != nil {
			coreDebugf("stop core before replacement failed: err=%v", err)
			return CoreConfig{}, err
		}
	}

	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return CoreConfig{}, errors.New("core service is shutting down")
	}
	if s.configGeneration != generation {
		s.mu.Unlock()
		if wasRunning {
			if _, restartErr := s.startCore("", coreType); restartErr != nil {
				return CoreConfig{}, fmt.Errorf("core configuration changed while downloading; restart core: %w", restartErr)
			}
		}
		return CoreConfig{}, errors.New("core configuration changed while downloading; please retry")
	}
	config, err = s.installCoreArchiveLocked(config, archivePath, targetVersion)
	s.mu.Unlock()
	if err != nil {
		coreDebugf("install downloaded core failed: type=%s version=%s err=%v", coreType, targetVersion, err)
	}

	if wasRunning {
		coreDebugf("restarting core after replacement: type=%s", coreType)
		restarted, restartErr := s.startCore(runArgs, coreType)
		if restartErr != nil {
			coreDebugf("restart core after replacement failed: err=%v", restartErr)
			if err != nil {
				return CoreConfig{}, fmt.Errorf("%v; restart sing-box core: %w", err, restartErr)
			}
			return CoreConfig{}, fmt.Errorf("restart sing-box core after update: %w", restartErr)
		}
		if err == nil {
			config = restarted
		}
	}
	return config, err
}

func (s *CoreService) downloadCoreArchive(currentVersion string, config CoreConfig) (CoreConfig, string, string, error) {
	var err error
	s.applyCurrentVersion(&config, currentVersion)
	if config.URLTemplate == "" {
		return CoreConfig{}, "", "", errors.New("core download URL has not been configured")
	}
	config.URLTemplate = canonicalMihomoURLTemplate(config)

	targetVersion := normalizeCoreVersion(config.LatestVersion)
	if isMihomoTestPlaceholderVersion(config, targetVersion) {
		targetVersion = ""
	}
	if targetVersion == "" {
		targetVersion = normalizeCoreVersion(config.ConfiguredVersion)
		if isMihomoTestPlaceholderVersion(config, targetVersion) {
			targetVersion = ""
		}
	}
	if targetVersion == "" {
		targetVersion, err = findLatestReleaseForConfig(config)
		if err != nil {
			return CoreConfig{}, "", "", err
		}
	}
	downloadURL := strings.ReplaceAll(config.URLTemplate, "{version}", targetVersion)
	coreDebugf("download target: type=%s channel=%s version=%s url=%q", config.CoreType, config.Channel, targetVersion, downloadURL)
	parsedURL, err := url.Parse(downloadURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return CoreConfig{}, "", "", errors.New("core download URL is invalid")
	}

	expectedSHA256 := ""
	if owner, repository, repositoryErr := githubRepository(config.URLTemplate); repositoryErr == nil {
		expectedSHA256, err = findReleaseAssetDigest(owner, repository, targetVersion, downloadURL)
		if err != nil {
			return CoreConfig{}, "", "", err
		}
		coreDebugf("release digest resolved: repository=%s/%s version=%s present=%t", owner, repository, targetVersion, expectedSHA256 != "")
	}

	archivePath, err := s.archiveTools().Download(downloadURL, expectedSHA256)
	if err != nil {
		return CoreConfig{}, "", "", err
	}
	return config, archivePath, targetVersion, nil
}

func (s *CoreService) installCoreArchiveLocked(config CoreConfig, archivePath, targetVersion string) (CoreConfig, error) {
	corePath := s.corePathFor(config.CoreType)
	coreDebugf("install archive: type=%s archive=%q target=%q", config.CoreType, archivePath, corePath)
	installed, err := s.archiveTools().Extract(archivePath, corePath, coreArchiveExecutableMatcher(config.CoreType))
	if err != nil {
		coreDebugf("extract archive failed: archive=%q err=%v", archivePath, err)
		return CoreConfig{}, err
	}
	if !installed {
		coreDebugf("replace archive completed without installation: target=%q", corePath)
		return CoreConfig{}, errors.New("sing-box core could not be replaced after it stopped")
	}

	config.CorePath = corePath
	installedVersion, versionDetail, versionErr := readCoreVersionDetail(corePath, config.CoreType)
	if versionErr != nil {
		coreDebugf("read installed core version failed: path=%q err=%v", corePath, versionErr)
		return CoreConfig{}, versionErr
	}
	coreDebugf("core replacement verified: type=%s installedVersion=%s", config.CoreType, installedVersion)
	if stat, statErr := os.Stat(corePath); statErr == nil {
		if s.versionCache == nil {
			s.versionCache = make(map[string]coreVersionCacheItem)
		}
		s.versionCache[config.CoreType] = coreVersionCacheItem{
			modTime: stat.ModTime(),
			size:    stat.Size(),
			version: installedVersion,
			detail:  versionDetail,
		}
	}
	config.Version = installedVersion
	config.VersionDetail = versionDetail
	config.Channel = coreChannel(installedVersion)
	config.InstalledVersion = installedVersion
	config.Installed = true
	config.LatestVersion = targetVersion
	config.UpdateAvailable = compareCoreVersions(mustParseCoreVersion(targetVersion), mustParseCoreVersion(installedVersion)) > 0
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
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
	configuredVersion := ""
	if !strings.Contains(tag, "{version}") {
		configuredVersion = normalizeCoreVersion(tag)
		if configuredVersion == "" && !strings.EqualFold(tag, "latest") && !isCoreStaticReleaseTag(tag) {
			return CoreConfig{}, errors.New("无法从地址识别版本号")
		}
		channel = coreChannel(tag)
		if !isCoreStaticReleaseTag(tag) {
			replacement := "{version}"
			if strings.HasPrefix(tag, "v") || strings.HasPrefix(tag, "V") {
				replacement = tag[:1] + "{version}"
			}
			parsedURL.Path = strings.Replace(parsedURL.Path, tag, replacement, 1)
		}
	}
	parsedURL.RawPath = ""
	templateURL := parsedURL.String()
	templateURL = strings.ReplaceAll(templateURL, "%7Bversion%7D", "{version}")
	templateURL = strings.ReplaceAll(templateURL, "%7bversion%7d", "{version}")

	return CoreConfig{
		URLTemplate:       templateURL,
		ConfiguredVersion: configuredVersion,
		Channel:           channel,
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
		match = coreBuildTagPattern.FindStringSubmatch(value)
		if len(match) < 2 {
			return "", fmt.Errorf("unsupported version %q", value)
		}
	}
	return match[1], nil
}

func coreChannel(version string) string {
	if testChannelPattern.MatchString(version) {
		return coreChannelTest
	}
	return coreChannelStable
}

func isCoreStaticReleaseTag(tag string) bool {
	return strings.EqualFold(strings.TrimSpace(tag), mihomoPrereleaseTag)
}

func normalizeCoreChannel(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case coreChannelStable:
		return coreChannelStable, nil
	case coreChannelTest:
		return coreChannelTest, nil
	default:
		return "", errors.New("核心渠道必须是稳定版或测试版")
	}
}

func githubRepository(template string) (string, string, error) {
	parsedURL, err := url.Parse(template)
	if err != nil || !strings.EqualFold(parsedURL.Hostname(), "github.com") {
		return "", "", errors.New("核心地址必须来自 github.com")
	}
	segments := pathSegments(parsedURL.Path)
	if len(segments) < 5 || !strings.EqualFold(segments[2], "releases") || !strings.EqualFold(segments[3], "download") || (!strings.Contains(segments[4], "{version}") && !isCoreStaticReleaseTag(segments[4])) {
		return "", "", errors.New("核心地址不是有效的 GitHub Release 通用地址")
	}
	if segments[0] == "" || segments[1] == "" {
		return "", "", errors.New("无法识别 GitHub 仓库")
	}
	return segments[0], segments[1], nil
}

func findLatestRelease(owner, repository, channel string) (string, error) {
	client := newCoreHTTPClient(30 * time.Second)
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repository))
	isMihomoPrerelease := strings.EqualFold(owner, "MetaCubeX") && strings.EqualFold(repository, "mihomo") && channel == coreChannelTest
	if isMihomoPrerelease {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), mihomoPrereleaseTag)
	} else if channel == coreChannelTest {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=3", url.PathEscape(owner), url.PathEscape(repository))
	}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("check core update: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "zashdesktop")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("check core update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("check core update: GitHub returned %s", response.Status)
	}
	if isMihomoPrerelease {
		var release githubRelease
		if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&release); err != nil {
			return "", fmt.Errorf("parse GitHub release: %w", err)
		}
		version := releaseVersion(release)
		if version == "" {
			return "", errors.New("Mihomo prerelease has no valid alpha build version")
		}
		return version, nil
	}
	if channel == coreChannelTest {
		var releases []githubRelease
		if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&releases); err != nil {
			return "", fmt.Errorf("parse GitHub releases: %w", err)
		}
		for _, release := range releases {
			version := releaseVersion(release)
			if version == "" || (!release.Prerelease && coreChannel(version) != coreChannelTest) {
				continue
			}
			return version, nil
		}
		return "", errors.New("no test core release found")
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("parse GitHub release: %w", err)
	}
	version := normalizeCoreVersion(release.TagName)
	if version == "" {
		return "", errors.New("GitHub release has no valid core version")
	}
	return version, nil
}

func releaseVersion(release githubRelease) string {
	for _, asset := range release.Assets {
		match := coreBuildAssetPattern.FindStringSubmatch(asset.Name)
		if len(match) >= 3 {
			return match[2]
		}
	}
	version := normalizeCoreVersion(release.TagName)
	if isGenericCoreBuildVersion(version) {
		return ""
	}
	return version
}

func isGenericCoreBuildVersion(version string) bool {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "alpha", "beta", "rc", "dev", "nightly", "preview":
		return true
	default:
		return false
	}
}

func isMihomoTestPlaceholderVersion(config CoreConfig, version string) bool {
	return normalizedCoreType(config.CoreType) == coreTypeMihomo && config.Channel == coreChannelTest && !mihomoTestVersionPattern.MatchString(strings.TrimSpace(version))
}

func canonicalMihomoURLTemplate(config CoreConfig) string {
	if normalizedCoreType(config.CoreType) != coreTypeMihomo {
		return config.URLTemplate
	}
	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil || !strings.EqualFold(owner, "MetaCubeX") || !strings.EqualFold(repository, "mihomo") {
		return config.URLTemplate
	}
	if config.Channel == coreChannelTest {
		return mihomoTestURLTemplate
	}
	return mihomoStableURLTemplate
}

func findLatestReleaseForConfig(config CoreConfig) (string, error) {
	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil {
		return "", err
	}
	return findLatestRelease(owner, repository, config.Channel)
}

func findReleaseAssetDigest(owner, repository, version, downloadURL string) (string, error) {
	client := newCoreHTTPClient(30 * time.Second)
	tags := make([]string, 0, 3)
	if parsedURL, err := url.Parse(downloadURL); err == nil {
		segments := pathSegments(parsedURL.Path)
		if len(segments) >= 5 {
			tags = append(tags, segments[4])
		}
	}
	tags = append(tags, "v"+version, version)
	seenTags := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, exists := seenTags[tag]; exists {
			continue
		}
		seenTags[tag] = struct{}{}
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(tag))
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "zashdesktop")
		response, err := client.Do(request)
		if err != nil {
			return "", err
		}
		var release githubRelease
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&release)
		response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			continue
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return "", fmt.Errorf("lookup core release: GitHub returned %s", response.Status)
		}
		if decodeErr != nil {
			return "", fmt.Errorf("parse GitHub release: %w", decodeErr)
		}
		assetName := path.Base(strings.SplitN(downloadURL, "?", 2)[0])
		for _, asset := range release.Assets {
			if asset.Name != assetName && !strings.HasSuffix(asset.BrowserDownloadURL, "/"+assetName) {
				continue
			}
			if strings.HasPrefix(strings.ToLower(asset.Digest), "sha256:") {
				return strings.TrimSpace(asset.Digest[len("sha256:"):]), nil
			}
			return "", nil
		}
		return "", fmt.Errorf("core download asset not found: %s", assetName)
	}
	return "", errors.New("core release not found")
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
		parsed.suffix = []string{strings.ToLower(version)}
		return parsed, nil
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

func (s *CoreService) applyCurrentVersion(config *CoreConfig, supplied string) {
	suppliedVersion := normalizeCoreVersion(supplied)
	corePath := s.corePathFor(config.CoreType)

	stat, err := os.Stat(corePath)
	if err != nil || stat.IsDir() {
		config.Installed = false
		config.InstalledVersion = ""
		config.Version = suppliedVersion
		if config.Channel == "" && suppliedVersion != "" {
			config.Channel = coreChannel(suppliedVersion)
		}
		return
	}

	config.Installed = true

	if suppliedVersion != "" {
		config.Version = suppliedVersion
		config.InstalledVersion = suppliedVersion
		if config.Channel == "" {
			config.Channel = coreChannel(suppliedVersion)
		}
		return
	}

	if s.versionCache == nil {
		s.versionCache = make(map[string]coreVersionCacheItem)
	}
	cached, ok := s.versionCache[config.CoreType]
	if ok && cached.modTime.Equal(stat.ModTime()) && cached.size == stat.Size() && cached.version != "" {
		config.Version = cached.version
		config.VersionDetail = cached.detail
		config.InstalledVersion = cached.version
		if config.Channel == "" {
			config.Channel = coreChannel(cached.version)
		}
		return
	}

	version, versionDetail, err := readCoreVersionDetail(corePath, config.CoreType)
	if err == nil && version != "" {
		s.versionCache[config.CoreType] = coreVersionCacheItem{
			modTime: stat.ModTime(),
			size:    stat.Size(),
			version: version,
			detail:  versionDetail,
		}
		config.Version = version
		config.VersionDetail = versionDetail
		config.InstalledVersion = version
		if config.Channel == "" {
			config.Channel = coreChannel(version)
		}
	} else if config.InstalledVersion != "" {
		config.Version = config.InstalledVersion
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
	if len(match) == 6 {
		return match[2]
	}
	match = coreBuildOutputPattern.FindStringSubmatch(value)
	if len(match) == 4 {
		return match[2]
	}
	return ""
}

func readCoreVersion(path string) (string, error) {
	version, _, err := readCoreVersionDetail(path, coreTypeSingBox)
	return version, err
}

func readCoreVersionDetail(corePath, coreType string) (string, string, error) {
	if !fileExists(corePath) {
		return "", "", errors.New("sing-box core is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	versionArgs := []string{"version"}
	if normalizedCoreType(coreType) == coreTypeMihomo {
		versionArgs = []string{"-v"}
	}
	command := exec.CommandContext(ctx, corePath, versionArgs...)
	configureCoreCommand(command)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("read sing-box core version: %w", err)
	}
	version := normalizeCoreVersion(string(output))
	if version == "" {
		return "", "", errors.New("unable to read sing-box core version")
	}
	return version, strings.TrimSpace(string(output)), nil
}

// -----------------------------------------------------------------------------
// Archive download and extraction tools
// -----------------------------------------------------------------------------

type coreArchiveTools struct {
	baseDir     string
	maxDownload int64
	maxBinary   int64
	logf        func(string, ...any)
}

func newCoreHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = systemProxy
	return &http.Client{Timeout: timeout, Transport: transport}
}

func (t coreArchiveTools) debugf(format string, args ...any) {
	if t.logf != nil {
		t.logf(format, args...)
	}
}

func (t coreArchiveTools) Download(downloadURL, expectedSHA256 string) (string, error) {
	t.debugf("HTTP download start: url=%q checksum=%t", downloadURL, expectedSHA256 != "")
	client := newCoreHTTPClient(20 * time.Minute)
	response, err := client.Get(downloadURL)
	if err != nil {
		t.debugf("HTTP download request failed: url=%q err=%v", downloadURL, err)
		return "", fmt.Errorf("download core: %w", err)
	}
	defer response.Body.Close()
	t.debugf("HTTP download response: status=%s contentLength=%d", response.Status, response.ContentLength)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.debugf("HTTP download rejected: status=%s", response.Status)
		return "", fmt.Errorf("download core: server returned %s", response.Status)
	}
	if response.ContentLength > t.maxDownload {
		t.debugf("HTTP download rejected: contentLength=%d limit=%d", response.ContentLength, t.maxDownload)
		return "", errors.New("core archive is too large")
	}

	suffix := ".archive"
	if parsed, parseErr := url.Parse(downloadURL); parseErr == nil {
		lowerPath := strings.ToLower(parsed.Path)
		switch {
		case strings.HasSuffix(lowerPath, ".tar.gz"):
			suffix = ".tar.gz"
		case strings.HasSuffix(lowerPath, ".zip"):
			suffix = ".zip"
		}
	}
	temporary, err := os.CreateTemp(t.baseDir, ".core-download-*"+suffix)
	if err != nil {
		t.debugf("create archive temp file failed: suffix=%q err=%v", suffix, err)
		return "", fmt.Errorf("create core archive: %w", err)
	}
	path := temporary.Name()
	t.debugf("archive temp file created: path=%q format=%s", path, suffix)
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
	}()

	var writer io.Writer = temporary
	digest := sha256.New()
	if expectedSHA256 != "" {
		writer = io.MultiWriter(temporary, digest)
	}
	written, err := io.Copy(writer, io.LimitReader(response.Body, t.maxDownload+1))
	if err != nil {
		t.debugf("save downloaded archive failed: path=%q bytes=%d err=%v", path, written, err)
		_ = temporary.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("save core archive: %w", err)
	}
	if written > t.maxDownload {
		t.debugf("downloaded archive exceeded limit: path=%q bytes=%d limit=%d", path, written, t.maxDownload)
		_ = temporary.Close()
		_ = os.Remove(path)
		return "", errors.New("core archive is too large")
	}
	if err := temporary.Close(); err != nil {
		t.debugf("close archive temp file failed: path=%q err=%v", path, err)
		_ = os.Remove(path)
		return "", fmt.Errorf("close core archive: %w", err)
	}
	if expectedSHA256 != "" {
		actualSHA256 := fmt.Sprintf("%x", digest.Sum(nil))
		if !strings.EqualFold(actualSHA256, expectedSHA256) {
			t.debugf("archive checksum mismatch: path=%q expected=%s actual=%s", path, expectedSHA256, actualSHA256)
			_ = os.Remove(path)
			return "", errors.New("core archive checksum does not match the release digest")
		}
		t.debugf("archive checksum verified: path=%q sha256=%s", path, actualSHA256)
	}
	temporary = nil
	t.debugf("HTTP download complete: path=%q bytes=%d", path, written)
	return path, nil
}

func (t coreArchiveTools) Extract(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	t.debugf("extract archive start: archive=%q target=%q", archivePath, targetPath)
	if isExecutable == nil {
		return false, errors.New("archive executable matcher is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.debugf("create core directory failed: path=%q err=%v", filepath.Dir(targetPath), err)
		return false, fmt.Errorf("create core directory: %w", err)
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz") {
		t.debugf("extract archive format: tar.gz")
		return t.extractTarGZ(archivePath, targetPath, isExecutable)
	}
	t.debugf("extract archive format: zip")
	return t.extractZIP(archivePath, targetPath, isExecutable)
}

func (t coreArchiveTools) extractZIP(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.debugf("open ZIP archive failed: archive=%q err=%v", archivePath, err)
		return false, fmt.Errorf("open core ZIP archive: %w", err)
	}
	defer archive.Close()
	t.debugf("ZIP archive opened: archive=%q entries=%d", archivePath, len(archive.File))

	var selected *zip.File
	for _, entry := range archive.File {
		name := filepath.Base(strings.ReplaceAll(entry.Name, "\\", "/"))
		if !isExecutable(name) || entry.FileInfo().IsDir() || entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.UncompressedSize64 > uint64(t.maxBinary) {
			continue
		}
		if selected == nil || strings.Count(entry.Name, "/") < strings.Count(selected.Name, "/") {
			selected = entry
		}
	}
	if selected == nil {
		t.debugf("ZIP executable not found: archive=%q", archivePath)
		return false, errors.New("core executable was not found in the ZIP archive")
	}
	t.debugf("ZIP executable selected: entry=%q size=%d", selected.Name, selected.UncompressedSize64)

	reader, err := selected.Open()
	if err != nil {
		t.debugf("open ZIP executable failed: entry=%q err=%v", selected.Name, err)
		return false, fmt.Errorf("read core executable: %w", err)
	}
	defer reader.Close()
	return t.installExecutable(reader, targetPath)
}

func (t coreArchiveTools) extractTarGZ(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.debugf("open TAR.GZ archive failed: archive=%q err=%v", archivePath, err)
		return false, fmt.Errorf("open core TAR.GZ archive: %w", err)
	}
	defer archiveFile.Close()

	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		t.debugf("open gzip stream failed: archive=%q err=%v", archivePath, err)
		return false, fmt.Errorf("open core gzip archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.debugf("read TAR archive failed: archive=%q err=%v", archivePath, nextErr)
			return false, fmt.Errorf("read core TAR archive: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > t.maxBinary || !isExecutable(filepath.Base(header.Name)) {
			continue
		}
		t.debugf("TAR executable selected: entry=%q size=%d", header.Name, header.Size)
		return t.installExecutable(io.LimitReader(tarReader, header.Size), targetPath)
	}
	t.debugf("TAR executable not found: archive=%q", archivePath)
	return false, errors.New("core executable was not found in the TAR.GZ archive")
}

func (t coreArchiveTools) installExecutable(reader io.Reader, targetPath string) (bool, error) {
	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".core-executable-*"+filepath.Ext(targetPath))
	if err != nil {
		t.debugf("create extracted executable temp file failed: target=%q err=%v", targetPath, err)
		return false, fmt.Errorf("create core file: %w", err)
	}
	temporaryPath := temporary.Name()
	t.debugf("extracted executable temp file created: path=%q target=%q", temporaryPath, targetPath)
	defer os.Remove(temporaryPath)

	written, err := io.Copy(temporary, io.LimitReader(reader, t.maxBinary+1))
	if err != nil {
		t.debugf("write extracted executable failed: path=%q bytes=%d err=%v", temporaryPath, written, err)
		_ = temporary.Close()
		return false, fmt.Errorf("extract core executable: %w", err)
	}
	if written == 0 || written > t.maxBinary {
		t.debugf("extracted executable size invalid: path=%q bytes=%d limit=%d", temporaryPath, written, t.maxBinary)
		_ = temporary.Close()
		return false, errors.New("core executable is invalid or too large")
	}
	if err := temporary.Chmod(0o755); err != nil {
		t.debugf("set extracted executable permissions failed: path=%q err=%v", temporaryPath, err)
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		t.debugf("close extracted executable temp file failed: path=%q err=%v", temporaryPath, err)
		return false, err
	}

	t.debugf("extracted executable ready for replacement: path=%q bytes=%d", temporaryPath, written)
	installed, err := t.replaceExecutable(temporaryPath, targetPath)
	if err != nil {
		t.debugf("executable replacement failed: source=%q target=%q err=%v", temporaryPath, targetPath, err)
		return false, err
	}
	if installed {
		t.debugf("executable replacement succeeded: target=%q", targetPath)
	} else {
		t.debugf("executable replacement deferred: target=%q", targetPath)
	}
	return installed, nil
}

func (t coreArchiveTools) replaceExecutable(sourcePath, targetPath string) (bool, error) {
	previousPath := targetPath + ".replacing"
	t.debugf("replace executable start: source=%q target=%q", sourcePath, targetPath)
	_ = os.Remove(previousPath)
	if fileExists(targetPath) {
		t.debugf("existing executable found, moving aside: path=%q backup=%q", targetPath, previousPath)
		if err := os.Rename(targetPath, previousPath); err != nil {
			if isFileLockedError(err) {
				t.debugf("existing executable is locked: path=%q", targetPath)
				return false, nil
			}
			t.debugf("move existing executable aside failed: path=%q err=%v", targetPath, err)
			return false, fmt.Errorf("prepare core replacement: %w", err)
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		t.debugf("move new executable into place failed: source=%q target=%q err=%v", sourcePath, targetPath, err)
		if fileExists(previousPath) {
			if restoreErr := os.Rename(previousPath, targetPath); restoreErr != nil {
				t.debugf("restore previous executable failed: backup=%q target=%q err=%v", previousPath, targetPath, restoreErr)
			} else {
				t.debugf("previous executable restored: target=%q", targetPath)
			}
		}
		return false, fmt.Errorf("replace core executable: %w", err)
	}
	_ = os.Remove(previousPath)
	t.debugf("replace executable complete: target=%q", targetPath)
	return true, nil
}

