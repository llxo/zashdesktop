package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
	testVerPattern    = regexp.MustCompile(`(?i)^(?:alpha|alpha-smart|beta|dev|rc|nightly|preview)(?:[-._][0-9a-z]+)*$`)
	testChanPattern   = regexp.MustCompile(`(?i)(^|[-._])(alpha|beta|rc|dev|nightly|preview)([-._]|\d|$)`)
	buildAssetPattern = regexp.MustCompile(`(?i)(^|-)((?:alpha|beta|rc|dev|nightly|preview)(?:[-._][0-9a-z]+)+)\.(?:zip|tar\.gz)$`)
)

// -----------------------------------------------------------------------------
// Core Network Transport
// -----------------------------------------------------------------------------

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
// Core Version Cache (Local & Remote)
// -----------------------------------------------------------------------------

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

type remoteReleaseCacheItem struct {
	version   string
	digest    string
	fetchedAt time.Time
}

func (s *CoreService) getCachedLatestRelease(owner, repository, channel string) (remoteReleaseCacheItem, bool) {
	s.remoteReleaseMu.Lock()
	defer s.remoteReleaseMu.Unlock()
	if s.remoteReleaseCache == nil {
		return remoteReleaseCacheItem{}, false
	}
	key := strings.ToLower(owner + "/" + repository + ":" + channel)
	item, ok := s.remoteReleaseCache[key]
	if !ok || item.version == "" || time.Since(item.fetchedAt) >= remoteReleaseCacheTTL {
		return remoteReleaseCacheItem{}, false
	}
	return item, true
}

func (s *CoreService) setCachedLatestRelease(owner, repository, channel, version, digest string) {
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
	s.remoteReleaseCache[key] = remoteReleaseCacheItem{
		version:   version,
		digest:    digest,
		fetchedAt: time.Now(),
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
		return "", "", errors.New("core URL must be from github.com")
	}
	segments := pathSegments(parsedURL.Path)
	if len(segments) < 5 || !strings.EqualFold(segments[2], "releases") || !strings.EqualFold(segments[3], "download") || (!strings.Contains(segments[4], "{version}") && !isCoreStaticReleaseTag(segments[4])) {
		return "", "", errors.New("core URL is not a valid GitHub release URL")
	}
	if segments[0] == "" || segments[1] == "" {
		return "", "", errors.New("unable to identify GitHub repository")
	}
	return segments[0], segments[1], nil
}

func isMihomoTestPlaceholderVersion(config CoreConfig, version string) bool {
	return normalizedCoreType(config.CoreType) == coreTypeMihomo && config.Channel == coreChannelTest && !testVerPattern.MatchString(strings.TrimSpace(version))
}

// -----------------------------------------------------------------------------
// Local Core Version Reading & Probing
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
		return "", errors.New("core channel must be stable or test")
	}
}

// -----------------------------------------------------------------------------
// Remote GitHub Release & Tag Detection
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

// fetchLatestCoreRelease 直接请求 GitHub Releases API，一次性解析出目标版本号和 SHA-256 摘要
func fetchLatestCoreRelease(owner, repository, channel string) (version string, sha256Digest string, err error) {
	client := newCoreHTTPClient(10 * time.Second)
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repository))

	isMihomoPre := (strings.EqualFold(owner, "MetaCubeX") || strings.EqualFold(owner, "vernesong")) && strings.EqualFold(repository, "mihomo") && channel == coreChannelTest
	if isMihomoPre {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), mihomoPrereleaseTag)
	} else if channel == coreChannelTest {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=3", url.PathEscape(owner), url.PathEscape(repository))
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("create core release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "zashdesktop")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("check core update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("check core update: GitHub returned %s", resp.Status)
	}

	var targetRelease githubRelease
	if channel == coreChannelTest && !isMihomoPre {
		var releases []githubRelease
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&releases); err != nil {
			return "", "", fmt.Errorf("parse GitHub releases: %w", err)
		}
		for _, r := range releases {
			if r.Prerelease || coreChannel(r.TagName) == coreChannelTest {
				targetRelease = r
				break
			}
		}
		if targetRelease.TagName == "" && len(releases) > 0 {
			targetRelease = releases[0]
		}
	} else {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&targetRelease); err != nil {
			return "", "", fmt.Errorf("parse GitHub release: %w", err)
		}
	}

	v := normalizeCoreVersion(targetRelease.TagName)
	if v == "" || isCoreStaticReleaseTag(targetRelease.TagName) {
		for _, asset := range targetRelease.Assets {
			if match := buildAssetPattern.FindStringSubmatch(asset.Name); len(match) >= 3 {
				v = match[2]
				break
			}
		}
	}
	if v == "" {
		return "", "", errors.New("GitHub release has no valid core version")
	}

	digest := ""
	for _, asset := range targetRelease.Assets {
		lower := strings.ToLower(asset.Name)
		if strings.Contains(lower, "windows") && strings.Contains(lower, "amd64") {
			if strings.HasPrefix(strings.ToLower(asset.Digest), "sha256:") {
				digest = strings.TrimSpace(asset.Digest[7:])
				break
			}
		}
	}

	return v, digest, nil
}

func findLatestReleaseForURL(downloadURLTemplate, channel string) (string, string, error) {
	owner, repository, err := githubRepository(downloadURLTemplate)
	if err != nil {
		return "", "", err
	}
	return fetchLatestCoreRelease(owner, repository, channel)
}

// -----------------------------------------------------------------------------
// Service Update Checking Methods
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

	var latest, digest string
	if !force {
		if cached, ok := s.getCachedLatestRelease(owner, repository, config.Channel); ok {
			latest = cached.version
			digest = cached.digest
		}
	}
	if latest == "" {
		var fErr error
		latest, digest, fErr = fetchLatestCoreRelease(owner, repository, config.Channel)
		if fErr != nil {
			debugLogf("release", "fetch latest release failed for %s/%s (%s): %v", owner, repository, config.Channel, fErr)
			return CoreConfig{}, fErr
		}
		s.setCachedLatestRelease(owner, repository, config.Channel, latest, digest)
	}

	config.LatestVersion = latest
	config.UpdateAvailable = isCoreUpdateAvailable(latest, config.Version, config.Channel)
	debugLogf("release", "check update result: type=%s current=%s latest=%s updateAvailable=%t", coreType, config.Version, config.LatestVersion, config.UpdateAvailable)
	return s.applyCheckedConfig(config)
}
