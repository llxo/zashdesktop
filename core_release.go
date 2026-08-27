package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	mihomoPrereleaseTag         = "Prerelease-Alpha"
	mihomoMetaTestURLTemplate   = "https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-compatible-{version}.zip"
	mihomoMetaStableURLTemplate = "https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-compatible-v{version}.zip"
	mihomoSmartTestURLTemplate  = "https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip"
	remoteReleaseCacheTTL       = 30 * time.Minute
)

var (
	semverPattern     = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)(?:[^0-9a-z]|$)`)
	buildVerPattern   = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])v?((?:alpha|beta|rc|dev|nightly|preview)(?:[-._][0-9a-z]+)*)(?:[^0-9a-z]|$)`)
	buildAssetPattern = regexp.MustCompile(`(?i)(^|-)((?:alpha|beta|rc|dev|nightly|preview)(?:[-._][0-9a-z]+)+)\.(?:zip|tar\.gz)$`)
	testVerPattern    = regexp.MustCompile(`(?i)^(?:alpha|alpha-smart|beta|dev|rc|nightly|preview)(?:[-._][0-9a-z]+)*$`)
	testChanPattern   = regexp.MustCompile(`(?i)(^|[-._])(alpha|beta|rc|dev|nightly|preview)([-._]|\d|$)`)
	githubProxies     = []string{
		"https://gh-proxy.org",
		"https://ghfast.top",
		"https://down.clashparty.org",
		"https://download.mihomo.party",
	}
)

func buildGitHubCandidateURLs(rawURL string) []string {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "https://github.com/") && !strings.HasPrefix(rawURL, "http://github.com/") {
		return []string{rawURL}
	}
	urls := make([]string, 1, 1+len(githubProxies))
	urls[0] = rawURL
	for _, proxy := range githubProxies {
		urls = append(urls, strings.TrimRight(proxy, "/")+"/"+rawURL)
	}
	return urls
}

func newCoreHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = systemProxy
	return &http.Client{Timeout: timeout, Transport: transport}
}

// -----------------------------------------------------------------------------
// Version Caching
// -----------------------------------------------------------------------------

type remoteReleaseCacheItem struct {
	version   string
	fetchedAt time.Time
}

func (s *CoreService) getCachedLatestRelease(owner, repository, channel string) (string, bool) {
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	if s.remoteReleaseCache == nil {
		return "", false
	}
	key := strings.ToLower(owner + "/" + repository + ":" + channel)
	item, ok := s.remoteReleaseCache[key]
	if !ok || item.version == "" || time.Since(item.fetchedAt) >= remoteReleaseCacheTTL {
		return "", false
	}
	return item.version, true
}

func (s *CoreService) setCachedLatestRelease(owner, repository, channel, version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	if s.remoteReleaseCache == nil {
		s.remoteReleaseCache = make(map[string]remoteReleaseCacheItem)
	}
	key := strings.ToLower(owner + "/" + repository + ":" + channel)
	s.remoteReleaseCache[key] = remoteReleaseCacheItem{version: version, fetchedAt: time.Now()}
}

func (s *CoreService) clearCachedLatestRelease(owner, repository, channel string) {
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	if s.remoteReleaseCache != nil {
		delete(s.remoteReleaseCache, strings.ToLower(owner+"/"+repository+":"+channel))
	}
}

func (s *CoreService) getCachedCoreVersion(coreType string) (coreVersionCacheItem, bool) {
	s.versionCacheMu.RLock()
	defer s.versionCacheMu.RUnlock()
	if s.versionCache == nil {
		return coreVersionCacheItem{}, false
	}
	item, ok := s.versionCache[coreType]
	return item, ok
}

func (s *CoreService) setCachedCoreVersion(coreType string, item coreVersionCacheItem) {
	s.versionCacheMu.Lock()
	defer s.versionCacheMu.Unlock()
	if s.versionCache == nil {
		s.versionCache = make(map[string]coreVersionCacheItem)
	}
	s.versionCache[coreType] = item
}

// -----------------------------------------------------------------------------
// Version Parsing, Normalization & Comparison
// -----------------------------------------------------------------------------

type coreVersion struct {
	major     int
	minor     int
	patch     int
	hasSemver bool
	suffix    []string
}

func stripArchiveExtension(v string) string {
	lower := strings.ToLower(v)
	for _, ext := range []string{".tar.gz", ".tar.xz", ".zip", ".tgz", ".gz", ".exe"} {
		if strings.HasSuffix(lower, ext) {
			return v[:len(v)-len(ext)]
		}
	}
	return v
}

func normalizeCoreVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	clean := stripArchiveExtension(value)
	if match := semverPattern.FindStringSubmatch(clean); len(match) >= 2 && match[1] != "" {
		return match[1]
	}
	if match := buildVerPattern.FindStringSubmatch(clean); len(match) >= 2 && match[1] != "" {
		return match[1]
	}
	return ""
}

func parseCoreVersionParts(value string) (coreVersion, error) {
	version := normalizeCoreVersion(value)
	if version == "" {
		return coreVersion{}, fmt.Errorf("unsupported version %q", value)
	}
	base := version
	var suffix []string
	if idx := strings.IndexByte(version, '-'); idx >= 0 {
		base = version[:idx]
		for _, part := range strings.FieldsFunc(version[idx+1:], func(r rune) bool { return r == '.' || r == '-' }) {
			if part != "" {
				suffix = append(suffix, part)
			}
		}
	}
	var parsed coreVersion
	n, _ := fmt.Sscanf(base, "%d.%d.%d", &parsed.major, &parsed.minor, &parsed.patch)
	if n >= 2 {
		parsed.hasSemver = true
		parsed.suffix = suffix
		return parsed, nil
	}
	parsed.hasSemver = false
	parsed.suffix = []string{strings.ToLower(version)}
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
	for i := 0; i < len(left.suffix) && i < len(right.suffix); i++ {
		lPart, rPart := left.suffix[i], right.suffix[i]
		lNum, lErr := strconv.Atoi(lPart)
		rNum, rErr := strconv.Atoi(rPart)
		if lErr == nil && rErr == nil {
			if lNum < rNum {
				return -1
			}
			if lNum > rNum {
				return 1
			}
			continue
		}
		if lErr == nil && rErr != nil {
			return -1
		}
		if lErr != nil && rErr == nil {
			return 1
		}
		if strings.ToLower(lPart) < strings.ToLower(rPart) {
			return -1
		}
		if strings.ToLower(lPart) > strings.ToLower(rPart) {
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

func isCoreUpdateAvailable(latest, current, channel string) bool {
	latest = strings.TrimSpace(latest)
	current = strings.TrimSpace(current)
	if latest == "" {
		return false
	}
	if current == "" {
		return true
	}
	if strings.EqualFold(latest, current) {
		return false
	}
	pLatest, lErr := parseCoreVersionParts(latest)
	pCurrent, cErr := parseCoreVersionParts(current)
	if lErr == nil && cErr == nil && pLatest.hasSemver && pCurrent.hasSemver {
		return compareCoreVersions(pLatest, pCurrent) > 0
	}
	return !strings.EqualFold(latest, current)
}

func coreChannel(version string) string {
	if testChanPattern.MatchString(version) {
		return coreChannelTest
	}
	return coreChannelStable
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

func isCoreStaticReleaseTag(tag string) bool {
	return strings.EqualFold(strings.TrimSpace(tag), mihomoPrereleaseTag)
}

// -----------------------------------------------------------------------------
// Core URL & GitHub Repository Helpers
// -----------------------------------------------------------------------------

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
	var filename string
	if len(segments) >= 6 {
		filename = segments[5]
	}

	channel := ""
	configuredVersion := ""
	if isCoreStaticReleaseTag(tag) {
		channel = coreChannelTest
		if filename != "" && !strings.Contains(filename, "{version}") {
			configuredVersion = normalizeCoreVersion(filename)
		}
	} else if !strings.Contains(tag, "{version}") {
		configuredVersion = normalizeCoreVersion(tag)
		if configuredVersion == "" && filename != "" {
			configuredVersion = normalizeCoreVersion(filename)
		}
		if configuredVersion == "" && !strings.EqualFold(tag, "latest") {
			return CoreConfig{}, errors.New("无法从地址识别版本号")
		}
		if tag != "" && !strings.EqualFold(tag, "latest") {
			channel = coreChannel(tag)
		} else if configuredVersion != "" {
			channel = coreChannel(configuredVersion)
		}

		if !strings.EqualFold(tag, "latest") {
			tagReplacement := "{version}"
			if strings.HasPrefix(tag, "v") || strings.HasPrefix(tag, "V") {
				tagReplacement = tag[:1] + "{version}"
			}
			segments[4] = tagReplacement
		}
	}

	if filename != "" && !strings.Contains(filename, "{version}") && configuredVersion != "" {
		if tag != "" && !isCoreStaticReleaseTag(tag) && strings.Contains(filename, tag) {
			tagReplacement := "{version}"
			if strings.HasPrefix(tag, "v") || strings.HasPrefix(tag, "V") {
				tagReplacement = tag[:1] + "{version}"
			}
			segments[5] = strings.Replace(filename, tag, tagReplacement, 1)
		} else if strings.Contains(filename, "v"+configuredVersion) {
			segments[5] = strings.Replace(filename, "v"+configuredVersion, "v{version}", 1)
		} else if strings.Contains(filename, configuredVersion) {
			segments[5] = strings.Replace(filename, configuredVersion, "{version}", 1)
		}
	}

	parsedURL.Path = "/" + strings.Join(segments, "/")
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

func pathSegments(p string) []string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			segments = append(segments, part)
		}
	}
	return segments
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

func canonicalMihomoURLTemplate(config CoreConfig) string {
	if normalizedCoreType(config.CoreType) != coreTypeMihomo {
		return config.URLTemplate
	}
	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil || !strings.EqualFold(repository, "mihomo") {
		return config.URLTemplate
	}
	if strings.EqualFold(owner, "vernesong") {
		return mihomoSmartTestURLTemplate
	}
	if strings.EqualFold(owner, "MetaCubeX") {
		if config.Channel == coreChannelTest {
			return mihomoMetaTestURLTemplate
		}
		return mihomoMetaStableURLTemplate
	}
	return config.URLTemplate
}

func isMihomoTestPlaceholderVersion(config CoreConfig, version string) bool {
	return normalizedCoreType(config.CoreType) == coreTypeMihomo && config.Channel == coreChannelTest && !testVerPattern.MatchString(strings.TrimSpace(version))
}

// -----------------------------------------------------------------------------
// GitHub Release Fetching
// -----------------------------------------------------------------------------

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

func fetchVersionTxt(rawURL string) (string, error) {
	client := newCoreHTTPClient(10 * time.Second)
	var lastErr error
	for _, candidate := range buildGitHubCandidateURLs(rawURL) {
		req, err := http.NewRequest(http.MethodGet, candidate, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "zashdesktop")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("server returned %s", resp.Status)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		version := strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
		if version != "" {
			return version, nil
		}
		lastErr = errors.New("empty version.txt")
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("failed to fetch version.txt from all sources")
}

func fetchLatestReleaseTagByRedirect(owner, repository string) (string, error) {
	rawURL := fmt.Sprintf("https://github.com/%s/%s/releases/latest", owner, repository)
	client := newCoreHTTPClient(10 * time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	var lastErr error
	for _, candidate := range buildGitHubCandidateURLs(rawURL) {
		req, err := http.NewRequest(http.MethodGet, candidate, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "zashdesktop")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()
		location := resp.Header.Get("Location")
		if (resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect) && location != "" {
			if idx := strings.Index(location, "/releases/tag/"); idx != -1 {
				tag := strings.TrimSpace(location[idx+len("/releases/tag/"):])
				if qIdx := strings.Index(tag, "?"); qIdx != -1 {
					tag = tag[:qIdx]
				}
				tag = strings.Trim(tag, "/")
				if unescaped, err := url.PathUnescape(tag); err == nil && unescaped != "" {
					tag = unescaped
				}
				if tag != "" {
					return normalizeCoreVersion(tag), nil
				}
			}
		}
		lastErr = fmt.Errorf("server returned %s without release tag redirect", resp.Status)
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("failed to fetch latest release tag redirect")
}

func findLatestRelease(owner, repository, channel string) (string, error) {
	if strings.EqualFold(repository, "mihomo") {
		if strings.EqualFold(owner, "vernesong") {
			if v, err := fetchVersionTxt("https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/version.txt"); err == nil && v != "" {
				return normalizeCoreVersion(v), nil
			}
		} else if strings.EqualFold(owner, "MetaCubeX") {
			url := "https://github.com/MetaCubeX/mihomo/releases/latest/download/version.txt"
			if channel == coreChannelTest {
				url = "https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/version.txt"
			}
			if v, err := fetchVersionTxt(url); err == nil && v != "" {
				return normalizeCoreVersion(v), nil
			}
		}
	} else if channel != coreChannelTest {
		if tag, err := fetchLatestReleaseTagByRedirect(owner, repository); err == nil && tag != "" {
			return tag, nil
		}
	}

	client := newCoreHTTPClient(30 * time.Second)
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repository))
	isMihomoPre := (strings.EqualFold(owner, "MetaCubeX") || strings.EqualFold(owner, "vernesong")) && strings.EqualFold(repository, "mihomo") && channel == coreChannelTest
	if isMihomoPre {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), mihomoPrereleaseTag)
	} else if channel == coreChannelTest {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=10", url.PathEscape(owner), url.PathEscape(repository))
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("check core update: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "zashdesktop")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("check core update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("check core update: GitHub returned %s", resp.Status)
	}

	if channel == coreChannelTest && !isMihomoPre {
		var releases []githubRelease
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&releases); err != nil {
			return "", fmt.Errorf("parse GitHub releases: %w", err)
		}
		for _, release := range releases {
			v := releaseVersion(release)
			if v != "" && (release.Prerelease || coreChannel(v) == coreChannelTest) {
				return v, nil
			}
		}
		return "", errors.New("no test core release found")
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("parse GitHub release: %w", err)
	}
	v := releaseVersion(release)
	if v == "" {
		v = normalizeCoreVersion(release.TagName)
	}
	if v == "" {
		return "", errors.New("GitHub release has no valid core version")
	}
	return v, nil
}

func isGenericCoreBuildVersion(version string) bool {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "alpha", "beta", "rc", "dev", "nightly", "preview", "prerelease-alpha", "prerelease":
		return true
	default:
		return false
	}
}

func releaseVersion(release githubRelease) string {
	tagVer := normalizeCoreVersion(release.TagName)
	if tagVer != "" && !isGenericCoreBuildVersion(tagVer) && !isCoreStaticReleaseTag(release.TagName) {
		return tagVer
	}
	for _, asset := range release.Assets {
		if match := buildAssetPattern.FindStringSubmatch(asset.Name); len(match) >= 3 {
			return match[2]
		}
	}
	if isGenericCoreBuildVersion(tagVer) {
		return ""
	}
	return tagVer
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
		if segments := pathSegments(parsedURL.Path); len(segments) >= 5 {
			tags = append(tags, segments[4])
		}
	}
	if version != "" {
		tags = append(tags, "v"+version, version)
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(tag))
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "zashdesktop")
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		var release githubRelease
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&release)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return "", fmt.Errorf("lookup core release: GitHub returned %s", resp.Status)
		}
		if decodeErr != nil {
			return "", fmt.Errorf("parse GitHub release: %w", decodeErr)
		}
		assetName := path.Base(strings.SplitN(downloadURL, "?", 2)[0])
		for _, asset := range release.Assets {
			if asset.Name == assetName || strings.HasSuffix(asset.BrowserDownloadURL, "/"+assetName) {
				if strings.HasPrefix(strings.ToLower(asset.Digest), "sha256:") {
					return strings.TrimSpace(asset.Digest[7:]), nil
				}
				return "", nil
			}
		}
		return "", fmt.Errorf("core download asset not found: %s", assetName)
	}
	return "", errors.New("core release not found")
}

// -----------------------------------------------------------------------------
// Service Update & Download Methods
// -----------------------------------------------------------------------------

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
	config.URLTemplate = canonicalMihomoURLTemplate(config)

	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil {
		if config.ConfiguredVersion == "" {
			return CoreConfig{}, err
		}
		config.LatestVersion = config.ConfiguredVersion
		config.UpdateAvailable = isCoreUpdateAvailable(config.LatestVersion, config.Version, config.Channel)
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
	config.UpdateAvailable = isCoreUpdateAvailable(latest, config.Version, config.Channel)
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
		coreDebugf("validate URL: release digest check returned: %v", err)
	}
	return config.URLTemplate, nil
}

func (s *CoreService) DownloadCore(currentVersion, rawCoreType string) (CoreConfig, error) {
	coreDebugf("download request: currentVersion=%q coreType=%q", currentVersion, rawCoreType)
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, archivePath, targetVersion, err := s.downloadCoreArchive(currentVersion, config)
	if err != nil {
		coreDebugf("download request failed: err=%v", err)
		return CoreConfig{}, err
	}
	defer os.Remove(archivePath)

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
	runningType := ""
	if s.process != nil {
		runningType = normalizedCoreType(s.processCoreType)
	} else if s.externalProcess != nil {
		runningType = normalizedCoreType(s.externalCoreType)
	}
	wasRunning := runningType == config.CoreType
	runArgs := config.RunArgs
	s.mu.Unlock()

	if wasRunning {
		coreDebugf("stopping core before replacement: type=%s", config.CoreType)
		if err := s.stopCoreProcess(); err != nil {
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
			_, _ = s.startCore("", coreType)
		}
		return CoreConfig{}, errors.New("core configuration changed while downloading; please retry")
	}
	config, err = s.installCoreArchiveLocked(config, archivePath, targetVersion)
	s.mu.Unlock()
	if err != nil {
		coreDebugf("install downloaded core failed: type=%s version=%s err=%v", coreType, targetVersion, err)
	}

	if wasRunning {
		restarted, restartErr := s.startCore(runArgs, coreType)
		if restartErr != nil {
			if err != nil {
				return CoreConfig{}, fmt.Errorf("%v; restart %s core: %w", err, coreType, restartErr)
			}
			return CoreConfig{}, fmt.Errorf("restart %s core after update: %w", coreType, restartErr)
		}
		if err == nil {
			config = restarted
		}
	}
	return config, err
}

func (s *CoreService) downloadCoreArchive(currentVersion string, config CoreConfig) (CoreConfig, string, string, error) {
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
		var err error
		targetVersion, err = findLatestReleaseForConfig(config)
		if err != nil {
			return CoreConfig{}, "", "", err
		}
	}
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" {
		return CoreConfig{}, "", "", errors.New("无法确定要下载的核心版本")
	}

	downloadURL := strings.ReplaceAll(config.URLTemplate, "{version}", targetVersion)
	parsedURL, err := url.Parse(downloadURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return CoreConfig{}, "", "", errors.New("core download URL is invalid")
	}

	expectedSHA256 := ""
	if owner, repo, err := githubRepository(config.URLTemplate); err == nil {
		if digest, dErr := findReleaseAssetDigest(owner, repo, targetVersion, downloadURL); dErr == nil {
			expectedSHA256 = digest
		}
	}

	archivePath, err := s.archiveTools().Download(downloadURL, expectedSHA256)
	if err != nil {
		return CoreConfig{}, "", "", err
	}
	return config, archivePath, targetVersion, nil
}

func (s *CoreService) installCoreArchiveLocked(config CoreConfig, archivePath, targetVersion string) (CoreConfig, error) {
	corePath := s.corePathFor(config.CoreType)
	installed, err := s.archiveTools().Extract(archivePath, corePath, coreArchiveExecutableMatcher(config.CoreType))
	if err != nil {
		return CoreConfig{}, err
	}
	if !installed {
		return CoreConfig{}, fmt.Errorf("%s core could not be replaced after it stopped", config.CoreType)
	}

	config.CorePath = corePath
	installedVersion, versionDetail, versionErr := readCoreVersionDetail(corePath, config.CoreType)
	if versionErr != nil {
		return CoreConfig{}, versionErr
	}
	if stat, statErr := os.Stat(corePath); statErr == nil {
		s.setCachedCoreVersion(config.CoreType, coreVersionCacheItem{
			modTime: stat.ModTime(),
			size:    stat.Size(),
			version: installedVersion,
			detail:  versionDetail,
		})
	}
	config.Version = installedVersion
	config.VersionDetail = versionDetail
	config.Channel = coreChannel(installedVersion)
	config.InstalledVersion = installedVersion
	config.Installed = true
	config.LatestVersion = targetVersion
	config.UpdateAvailable = isCoreUpdateAvailable(targetVersion, installedVersion, config.Channel)
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

// -----------------------------------------------------------------------------
// Core Execution & Version Reading
// -----------------------------------------------------------------------------

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

	if cached, ok := s.getCachedCoreVersion(config.CoreType); ok && cached.modTime.Equal(stat.ModTime()) && cached.size == stat.Size() && cached.version != "" {
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
		s.setCachedCoreVersion(config.CoreType, coreVersionCacheItem{
			modTime: stat.ModTime(),
			size:    stat.Size(),
			version: version,
			detail:  versionDetail,
		})
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

func readCoreVersion(path string) (string, error) {
	v, _, err := readCoreVersionDetail(path, coreTypeSingBox)
	return v, err
}

func readCoreVersionDetail(corePath, coreType string) (string, string, error) {
	if !fileExists(corePath) {
		return "", "", fmt.Errorf("%s core is not installed", coreType)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	versionArgs := []string{"version"}
	if normalizedCoreType(coreType) == coreTypeMihomo {
		versionArgs = []string{"-v"}
	}
	cmd := exec.CommandContext(ctx, corePath, versionArgs...)
	configureCoreCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("read %s core version: %w", coreType, err)
	}
	version := normalizeCoreVersion(string(output))
	if version == "" {
		return "", "", fmt.Errorf("unable to read %s core version", coreType)
	}
	return version, strings.TrimSpace(string(output)), nil
}

// -----------------------------------------------------------------------------
// Archive Download, Extraction & Replacement
// -----------------------------------------------------------------------------

type coreArchiveTools struct {
	baseDir     string
	maxDownload int64
	maxBinary   int64
	logf        func(string, ...any)
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
		return name == prefix || strings.HasPrefix(name, prefix+"-") || strings.HasPrefix(name, prefix+"_")
	}
}

func coreExecutableNameFor(coreType string) string {
	baseName := coreExecutableBaseName
	if normalizedCoreType(coreType) == coreTypeMihomo {
		baseName = mihomoExecutableName
	}
	return baseName + ".exe"
}

func (t coreArchiveTools) debugf(format string, args ...any) {
	if t.logf != nil {
		t.logf(format, args...)
	}
}

func (t coreArchiveTools) Download(downloadURL, expectedSHA256 string) (string, error) {
	var lastErr error
	for _, candidate := range buildGitHubCandidateURLs(downloadURL) {
		path, err := t.downloadSingle(candidate, expectedSHA256)
		if err == nil {
			return path, nil
		}
		t.debugf("candidate download failed: url=%q err=%v", candidate, err)
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("download failed from all sources")
}

func (t coreArchiveTools) downloadSingle(downloadURL, expectedSHA256 string) (string, error) {
	t.debugf("HTTP download start: url=%q checksum=%t", downloadURL, expectedSHA256 != "")
	client := newCoreHTTPClient(20 * time.Minute)
	resp, err := client.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download core: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download core: server returned %s", resp.Status)
	}
	if resp.ContentLength > t.maxDownload {
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
	temp, err := os.CreateTemp(t.baseDir, ".core-download-*"+suffix)
	if err != nil {
		return "", fmt.Errorf("create core archive: %w", err)
	}
	path := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
	}()

	var writer io.Writer = temp
	digest := sha256.New()
	if expectedSHA256 != "" {
		writer = io.MultiWriter(temp, digest)
	}
	written, err := io.Copy(writer, io.LimitReader(resp.Body, t.maxDownload+1))
	if err != nil || written > t.maxDownload {
		_ = temp.Close()
		_ = os.Remove(path)
		if err != nil {
			return "", fmt.Errorf("save core archive: %w", err)
		}
		return "", errors.New("core archive is too large")
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close core archive: %w", err)
	}
	if expectedSHA256 != "" {
		actualSHA256 := hex.EncodeToString(digest.Sum(nil))
		if !strings.EqualFold(actualSHA256, expectedSHA256) {
			_ = os.Remove(path)
			return "", errors.New("core archive checksum does not match the release digest")
		}
	}
	temp = nil
	t.debugf("HTTP download complete: path=%q bytes=%d", path, written)
	return path, nil
}

func (t coreArchiveTools) Extract(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	t.debugf("extract archive start: archive=%q target=%q", archivePath, targetPath)
	if isExecutable == nil {
		return false, errors.New("archive executable matcher is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return false, fmt.Errorf("create core directory: %w", err)
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz") {
		return t.extractTarGZ(archivePath, targetPath, isExecutable)
	}
	return t.extractZIP(archivePath, targetPath, isExecutable)
}

func (t coreArchiveTools) extractZIP(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return false, fmt.Errorf("open core ZIP archive: %w", err)
	}
	defer archive.Close()

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
		return false, errors.New("core executable was not found in the ZIP archive")
	}

	reader, err := selected.Open()
	if err != nil {
		return false, fmt.Errorf("read core executable: %w", err)
	}
	defer reader.Close()
	return t.installExecutable(reader, targetPath)
}

func (t coreArchiveTools) extractTarGZ(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return false, fmt.Errorf("open core TAR.GZ archive: %w", err)
	}
	defer archiveFile.Close()

	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return false, fmt.Errorf("open core gzip archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, fmt.Errorf("read core TAR archive: %w", err)
		}
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size <= 0 || header.Size > t.maxBinary {
			continue
		}
		name := filepath.Base(strings.ReplaceAll(header.Name, "\\", "/"))
		if !isExecutable(name) {
			continue
		}
		return t.installExecutable(io.LimitReader(tarReader, header.Size), targetPath)
	}
	return false, errors.New("core executable was not found in the TAR.GZ archive")
}

func (t coreArchiveTools) installExecutable(reader io.Reader, targetPath string) (bool, error) {
	temp, err := os.CreateTemp(filepath.Dir(targetPath), ".core-executable-*"+filepath.Ext(targetPath))
	if err != nil {
		return false, fmt.Errorf("create core file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	written, err := io.Copy(temp, io.LimitReader(reader, t.maxBinary+1))
	if err != nil || written == 0 || written > t.maxBinary {
		_ = temp.Close()
		if err != nil {
			return false, fmt.Errorf("extract core executable: %w", err)
		}
		return false, errors.New("core executable is invalid or too large")
	}
	_ = temp.Chmod(0o755)
	if err := temp.Close(); err != nil {
		return false, err
	}
	return t.replaceExecutable(tempPath, targetPath)
}

func (t coreArchiveTools) replaceExecutable(sourcePath, targetPath string) (bool, error) {
	previousPath := targetPath + ".replacing"
	_ = os.Remove(previousPath)
	if fileExists(targetPath) {
		if err := os.Rename(targetPath, previousPath); err != nil {
			if isFileLockedError(err) {
				return false, nil
			}
			return false, fmt.Errorf("prepare core replacement: %w", err)
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		if fileExists(previousPath) {
			_ = os.Rename(previousPath, targetPath)
		}
		return false, fmt.Errorf("replace core executable: %w", err)
	}
	_ = os.Remove(previousPath)
	return true, nil
}
