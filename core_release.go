package main

import (
	"archive/zip"
	"bytes"
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
	singBoxDefaultURLTemplate   = "https://github.com/llxo/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip"
	mihomoPrereleaseTag         = "Prerelease-Alpha"
	mihomoMetaTestURLTemplate   = "https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-compatible-{version}.zip"
	mihomoMetaStableURLTemplate = "https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-compatible-v{version}.zip"
	mihomoSmartTestURLTemplate  = "https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip"
	remoteReleaseCacheTTL       = 30 * time.Minute
)

func defaultCoreURLTemplate(coreType, channel string) string {
	if normalizedCoreType(coreType) == coreTypeMihomo {
		if channel == coreChannelTest {
			return mihomoMetaTestURLTemplate
		}
		return mihomoMetaStableURLTemplate
	}
	return singBoxDefaultURLTemplate
}

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
	urls := make([]string, 0, 1+len(githubProxies))
	for _, proxy := range githubProxies {
		urls = append(urls, strings.TrimRight(proxy, "/")+"/"+rawURL)
	}
	urls = append(urls, rawURL)
	return urls
}

type fallbackTransport struct {
	proxyTransport  *http.Transport
	directTransport *http.Transport
}

func newFallbackTransport() *fallbackTransport {
	pt := http.DefaultTransport.(*http.Transport).Clone()
	pt.Proxy = systemProxy

	dt := http.DefaultTransport.(*http.Transport).Clone()
	dt.Proxy = nil

	return &fallbackTransport{
		proxyTransport:  pt,
		directTransport: dt,
	}
}

var sharedCoreTransport = newFallbackTransport()

func isProxyFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "proxyconnect") ||
		strings.Contains(msg, "proxy error") ||
		strings.Contains(msg, "actively refused") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "socks connect") ||
		strings.Contains(msg, "connectex")
}

func (f *fallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	proxyURL, err := systemProxy(req)
	if err != nil || proxyURL == nil {
		return f.directTransport.RoundTrip(req)
	}

	var bodyBytes []byte
	if req.Body != nil && req.GetBody == nil {
		var readErr error
		bodyBytes, readErr = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	resp, proxyErr := f.proxyTransport.RoundTrip(req)
	if proxyErr == nil {
		return resp, nil
	}

	if isProxyFailure(proxyErr) {
		debugLogf("system", "system proxy %s connection failed (%v), silently falling back to direct connection for %s", proxyURL, proxyErr, req.URL.Redacted())
		invalidateProxySettingsCache()
		if req.GetBody != nil {
			newBody, getBodyErr := req.GetBody()
			if getBodyErr == nil {
				req.Body = newBody
			}
		}
		return f.directTransport.RoundTrip(req)
	}

	return nil, proxyErr
}

func newCoreHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: sharedCoreTransport}
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

func (s *CoreService) clearRemoteReleaseCache() {
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	s.remoteReleaseCache = make(map[string]remoteReleaseCacheItem)
}

func (s *CoreService) getCachedCoreVersion(coreType, channel string) (coreVersionCacheItem, bool) {
	s.versionCacheMu.RLock()
	defer s.versionCacheMu.RUnlock()
	if s.versionCache == nil {
		return coreVersionCacheItem{}, false
	}
	key := normalizedCoreType(coreType) + ":" + strings.ToLower(strings.TrimSpace(channel))
	item, ok := s.versionCache[key]
	return item, ok
}

func (s *CoreService) setCachedCoreVersion(coreType, channel string, item coreVersionCacheItem) {
	s.versionCacheMu.Lock()
	defer s.versionCacheMu.Unlock()
	if s.versionCache == nil {
		s.versionCache = make(map[string]coreVersionCacheItem)
	}
	key := normalizedCoreType(coreType) + ":" + strings.ToLower(strings.TrimSpace(channel))
	s.versionCache[key] = item
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

func findLatestReleaseForURL(downloadURLTemplate, channel string) (string, error) {
	owner, repository, err := githubRepository(downloadURLTemplate)
	if err != nil {
		return "", err
	}
	return findLatestRelease(owner, repository, channel)
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

func (s *CoreService) CheckUpdate(rawURL, rawCoreType string) (CoreConfig, error) {
	return s.checkUpdateInternal(rawURL, rawCoreType, false)
}

func (s *CoreService) ForceCheckUpdate(rawURL, rawCoreType string) (CoreConfig, error) {
	return s.checkUpdateInternal(rawURL, rawCoreType, true)
}

func (s *CoreService) checkUpdateInternal(rawURL, rawCoreType string, force bool) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		debugLogf("release", "check update normalize coreType failed: %v", err)
		return CoreConfig{}, err
	}
	config, _, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("release", "check update loadConfigSnapshot failed: %v", err)
		return CoreConfig{}, err
	}
	s.applyCurrentVersion(&config, "")

	downloadURL := strings.TrimSpace(rawURL)
	if downloadURL == "" {
		downloadURL = defaultCoreURLTemplate(config.CoreType, config.Channel)
	}

	owner, repository, err := githubRepository(downloadURL)
	if err != nil {
		debugLogf("release", "parse github repository from %q failed: %v", downloadURL, err)
		return CoreConfig{}, err
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
			debugLogf("release", "find latest release failed for %s/%s (%s): %v", owner, repository, config.Channel, err)
			return CoreConfig{}, err
		}
		s.setCachedLatestRelease(owner, repository, config.Channel, latest)
	}

	config.LatestVersion = latest
	config.UpdateAvailable = isCoreUpdateAvailable(latest, config.Version, config.Channel)
	debugLogf("release", "check update result: type=%s current=%s latest=%s updateAvailable=%t", coreType, config.Version, config.LatestVersion, config.UpdateAvailable)
	return s.applyCheckedConfig(config)
}

func (s *CoreService) DownloadCore(rawURL, rawCoreType string) (CoreConfig, error) {
	coreDebugf("download request: rawURL=%q coreType=%q", rawURL, rawCoreType)
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, _, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}

	config, archivePath, targetVersion, err := s.downloadCoreArchive(rawURL, config)
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
	if currentConfig, err := s.loadConfigForTypeLocked(coreType); err == nil {
		config = currentConfig
	}
	s.detectInheritedProcessLocked(config.CoreType)
	runningType := ""
	if s.process != nil {
		runningType = normalizedCoreType(s.processCoreType)
	} else if s.inheritedProcess != nil {
		runningType = normalizedCoreType(s.inheritedCoreType)
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
	if currentConfig, err := s.loadConfigForTypeLocked(coreType); err == nil {
		config = currentConfig
	}
	config, err = s.installCoreArchiveLocked(config, archivePath, targetVersion)
	s.mu.Unlock()
	if err != nil {
		coreDebugf("install downloaded core failed: type=%s version=%s err=%v", coreType, targetVersion, err)
	}

	if wasRunning {
		restarted, restartErr := s.startCore(runArgs, coreType, false)
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

func (s *CoreService) downloadCoreArchive(rawURL string, config CoreConfig) (CoreConfig, string, string, error) {
	s.applyCurrentVersion(&config, "")

	downloadURLTemplate := strings.TrimSpace(rawURL)
	if downloadURLTemplate == "" {
		downloadURLTemplate = defaultCoreURLTemplate(config.CoreType, config.Channel)
	}

	targetVersion := normalizeCoreVersion(config.LatestVersion)
	if isMihomoTestPlaceholderVersion(config, targetVersion) {
		targetVersion = ""
	}
	if targetVersion == "" {
		var err error
		targetVersion, err = findLatestReleaseForURL(downloadURLTemplate, config.Channel)
		if err != nil {
			return CoreConfig{}, "", "", err
		}
	}
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" {
		return CoreConfig{}, "", "", errors.New("无法确定要下载的核心版本")
	}

	downloadURL := strings.ReplaceAll(downloadURLTemplate, "{version}", targetVersion)
	parsedURL, err := url.Parse(downloadURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return CoreConfig{}, "", "", errors.New("core download URL is invalid")
	}

	expectedSHA256 := ""
	if owner, repo, err := githubRepository(downloadURLTemplate); err == nil {
		if digest, dErr := findReleaseAssetDigest(owner, repo, targetVersion, downloadURL); dErr == nil {
			expectedSHA256 = digest
		}
	}

	archivePath, err := downloadArchiveWithFallback(downloadURL, expectedSHA256, s.executableDir)
	if err != nil {
		return CoreConfig{}, "", "", err
	}
	return config, archivePath, targetVersion, nil
}

func (s *CoreService) installCoreArchiveLocked(config CoreConfig, archivePath, targetVersion string) (CoreConfig, error) {
	corePath := s.corePathFor(config.CoreType, config.Channel)
	if err := extractAndReplaceCoreExe(archivePath, corePath, config.CoreType); err != nil {
		return CoreConfig{}, err
	}

	config.CorePath = corePath
	installedVersion, versionDetail, versionErr := readCoreVersionDetail(corePath, config.CoreType)
	if versionErr != nil {
		return CoreConfig{}, versionErr
	}
	if stat, statErr := os.Stat(corePath); statErr == nil {
		s.setCachedCoreVersion(config.CoreType, config.Channel, coreVersionCacheItem{
			modTime: stat.ModTime(),
			size:    stat.Size(),
			version: installedVersion,
			detail:  versionDetail,
		})
	}
	config.Version = installedVersion
	config.VersionDetail = versionDetail
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
	if config.Channel == "" {
		config.Channel = coreChannelStable
	}
	suppliedVersion := normalizeCoreVersion(supplied)
	corePath := s.corePathFor(config.CoreType, config.Channel)
	config.CorePath = corePath

	stat, err := os.Stat(corePath)
	if err != nil || stat.IsDir() {
		config.Installed = false
		config.InstalledVersion = ""
		config.Version = suppliedVersion
		return
	}

	config.Installed = true
	if suppliedVersion != "" {
		config.Version = suppliedVersion
		config.InstalledVersion = suppliedVersion
		return
	}

	if cached, ok := s.getCachedCoreVersion(config.CoreType, config.Channel); ok && cached.modTime.Equal(stat.ModTime()) && cached.size == stat.Size() {
		if cached.version != "" {
			config.Version = cached.version
			config.VersionDetail = cached.detail
			config.InstalledVersion = cached.version
		} else if config.InstalledVersion != "" {
			config.Version = config.InstalledVersion
		}
		return
	}

	version, versionDetail, err := readCoreVersionDetail(corePath, config.CoreType)
	s.setCachedCoreVersion(config.CoreType, config.Channel, coreVersionCacheItem{
		modTime: stat.ModTime(),
		size:    stat.Size(),
		version: version,
		detail:  versionDetail,
	})
	if err == nil && version != "" {
		config.Version = version
		config.VersionDetail = versionDetail
		config.InstalledVersion = version
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
		debugLogf("release", "execute %s %v failed: %v", corePath, versionArgs, err)
		return "", "", fmt.Errorf("read %s core version: %w", coreType, err)
	}
	version := normalizeCoreVersion(string(output))
	if version == "" {
		debugLogf("release", "unable to parse version from output: %q", string(output))
		return "", "", fmt.Errorf("unable to read %s core version", coreType)
	}
	return version, strings.TrimSpace(string(output)), nil
}

// -----------------------------------------------------------------------------
// Archive Download, Extraction & Replacement
// -----------------------------------------------------------------------------

func coreExecutableNameFor(coreType, channel string) string {
	baseName := coreExecutableBaseName
	if normalizedCoreType(coreType) == coreTypeMihomo {
		baseName = mihomoExecutableName
	}
	if strings.EqualFold(strings.TrimSpace(channel), coreChannelTest) {
		return baseName + "-latest.exe"
	}
	return baseName + ".exe"
}

func isCoreArchiveExecutable(name, coreType string) bool {
	prefix := coreExecutableBaseName
	if normalizedCoreType(coreType) == coreTypeMihomo {
		prefix = mihomoExecutableName
	}
	clean := strings.ToLower(strings.TrimSuffix(filepath.Base(name), ".exe"))
	return clean == prefix || strings.HasPrefix(clean, prefix+"-") || strings.HasPrefix(clean, prefix+"_")
}

func downloadArchiveWithFallback(downloadURL, expectedSHA256, baseDir string) (string, error) {
	var lastErr error
	for _, candidate := range buildGitHubCandidateURLs(downloadURL) {
		path, err := downloadSingleArchive(candidate, expectedSHA256, baseDir)
		if err == nil {
			return path, nil
		}
		coreDebugf("candidate download failed: url=%q err=%v", candidate, err)
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("download failed from all sources")
}

func downloadSingleArchive(downloadURL, expectedSHA256, baseDir string) (string, error) {
	coreDebugf("HTTP download start: url=%q checksum=%t", downloadURL, expectedSHA256 != "")
	client := newCoreHTTPClient(20 * time.Minute)
	resp, err := client.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download core: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download core: server returned %s", resp.Status)
	}
	if resp.ContentLength > maxCoreDownload {
		return "", errors.New("core archive is too large")
	}

	temp, err := os.CreateTemp(baseDir, ".core-download-*.zip")
	if err != nil {
		return "", fmt.Errorf("create core archive: %w", err)
	}
	path := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
			_ = os.Remove(path)
		}
	}()

	var writer io.Writer = temp
	digest := sha256.New()
	if expectedSHA256 != "" {
		writer = io.MultiWriter(temp, digest)
	}

	written, err := io.Copy(writer, io.LimitReader(resp.Body, maxCoreDownload+1))
	if err != nil || written > maxCoreDownload {
		if err != nil {
			return "", fmt.Errorf("save core archive: %w", err)
		}
		return "", errors.New("core archive is too large")
	}

	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close core archive: %w", err)
	}
	if expectedSHA256 != "" {
		actualSHA256 := hex.EncodeToString(digest.Sum(nil))
		if !strings.EqualFold(actualSHA256, expectedSHA256) {
			return "", errors.New("core archive checksum does not match the release digest")
		}
	}
	temp = nil
	coreDebugf("HTTP download complete: path=%q bytes=%d", path, written)
	return path, nil
}

func extractAndReplaceCoreExe(archivePath, targetPath, coreType string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open core ZIP archive: %w", err)
	}
	defer archive.Close()

	var selected *zip.File
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() || entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.UncompressedSize64 > uint64(maxCoreBinary) {
			continue
		}
		if isCoreArchiveExecutable(entry.Name, coreType) {
			if selected == nil || strings.Count(entry.Name, "/") < strings.Count(selected.Name, "/") {
				selected = entry
			}
		}
	}
	if selected == nil {
		return fmt.Errorf("core executable not found in ZIP archive for %s", coreType)
	}

	reader, err := selected.Open()
	if err != nil {
		return fmt.Errorf("read core executable from zip: %w", err)
	}
	defer reader.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create core directory: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(targetPath), ".core-executable-*.exe")
	if err != nil {
		return fmt.Errorf("create temp core executable: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()

	written, err := io.Copy(temp, io.LimitReader(reader, maxCoreBinary+1))
	if err != nil || written == 0 || written > maxCoreBinary {
		if err != nil {
			return fmt.Errorf("extract core executable: %w", err)
		}
		return errors.New("core executable is invalid or too large")
	}
	_ = temp.Chmod(0o755)
	if err := temp.Close(); err != nil {
		return err
	}
	temp = nil

	return replaceExecutable(tempPath, targetPath)
}

func replaceExecutable(sourcePath, targetPath string) error {
	defer os.Remove(sourcePath)
	previousPath := targetPath + ".replacing"
	_ = os.Remove(previousPath)
	if fileExists(targetPath) {
		if err := os.Rename(targetPath, previousPath); err != nil {
			if isFileLockedError(err) {
				coreDebugf("target file %q is locked (sharing violation)", targetPath)
				return fmt.Errorf("target core executable is locked: %w", err)
			}
			coreDebugf("backup target file %q failed: %v", targetPath, err)
			return fmt.Errorf("prepare core replacement: %w", err)
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		coreDebugf("move source file %q to %q failed: %v", sourcePath, targetPath, err)
		if fileExists(previousPath) {
			_ = os.Rename(previousPath, targetPath)
		}
		return fmt.Errorf("replace core executable: %w", err)
	}
	_ = os.Remove(previousPath)
	coreDebugf("successfully replaced core binary at %q", targetPath)
	return nil
}
