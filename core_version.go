package main

import (
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
	semverPattern = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9a-zA-Z.]+)?`)
	alphaPattern  = regexp.MustCompile(`alpha(?:-smart)?-[0-9a-zA-Z]+`)
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

	resp, proxyErr := f.proxyTransport.RoundTrip(req)
	if proxyErr == nil {
		return resp, nil
	}

	if isProxyFailure(proxyErr) {
		debugLogf("system", "system proxy %s connection failed (%v), silently falling back to direct connection for %s", proxyURL, proxyErr, req.URL.Redacted())
		invalidateProxySettingsCache()
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

var githubRepoPattern = regexp.MustCompile(`(?i)github\.com/([^/]+)/([^/]+)`)

func parseGitHubRepo(template string) (string, string) {
	m := githubRepoPattern.FindStringSubmatch(template)
	if len(m) >= 3 {
		return m[1], m[2]
	}
	return "", ""
}

func isMihomoTestPlaceholderVersion(config CoreConfig, version string) bool {
	return normalizedCoreType(config.CoreType) == coreTypeMihomo && config.Channel == coreChannelTest && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(version)), "alpha")
}

// -----------------------------------------------------------------------------
// Local Core Version Reading & Probing
// -----------------------------------------------------------------------------

func (s *CoreService) applyCurrentVersion(config *CoreConfig, supplied string) {
	if config.Channel == "" {
		config.Channel = coreChannelStable
	}
	defer func() {
		if config.LatestVersion == "" {
			downloadURL := defaultCoreURLTemplate(config.CoreType, config.Channel)
			owner, repository := parseGitHubRepo(downloadURL)
			if cached, ok := s.getCachedLatestRelease(owner, repository, config.Channel); ok {
				config.LatestVersion = cached.version
				config.UpdateAvailable = isCoreUpdateAvailable(cached.version, config.Version, config.Channel)
			}
		}
	}()
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

func normalizeCoreVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimSuffix(value, ".zip")
	if v := semverPattern.FindString(value); v != "" {
		return v
	}
	if v := alphaPattern.FindString(value); v != "" {
		return v
	}
	return strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V")
}

// compareVersions 比较两个版本号，遵循标准版本主次数字递进规则。v1 > v2 返回 1，v1 < v2 返回 -1，相等返回 0
func compareVersions(v1, v2 string) int {
	v1 = normalizeCoreVersion(v1)
	v2 = normalizeCoreVersion(v2)
	if v1 == v2 {
		return 0
	}
	p1 := strings.Split(strings.Split(v1, "-")[0], ".")
	p2 := strings.Split(strings.Split(v2, "-")[0], ".")
	for i := 0; i < len(p1) || i < len(p2); i++ {
		var n1, n2 int
		if i < len(p1) {
			n1, _ = strconv.Atoi(p1[i])
		}
		if i < len(p2) {
			n2, _ = strconv.Atoi(p2[i])
		}
		if n1 != n2 {
			if n1 > n2 {
				return 1
			}
			return -1
		}
	}
	hasPre1 := strings.Contains(v1, "-")
	hasPre2 := strings.Contains(v2, "-")
	if !hasPre1 && hasPre2 {
		return 1
	}
	if hasPre1 && !hasPre2 {
		return -1
	}
	return strings.Compare(v1, v2)
}

func isCoreUpdateAvailable(latest, current, channel string) bool {
	latest = normalizeCoreVersion(latest)
	current = normalizeCoreVersion(current)
	if latest == "" {
		return false
	}
	if current == "" {
		return true
	}
	return compareVersions(latest, current) > 0
}

func coreChannel(version string) string {
	v := strings.ToLower(version)
	if strings.Contains(v, "alpha") || strings.Contains(v, "beta") || strings.Contains(v, "rc") {
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

type fetchReleaseResult struct {
	url     string
	version string
	digest  string
	err     error
}

func buildGitHubAPIURLs(endpoint string) []string {
	endpoint = strings.TrimSpace(endpoint)
	urls := make([]string, 0, 1+len(githubDownloadProxies))
	urls = append(urls, endpoint)
	for _, proxy := range githubDownloadProxies {
		urls = append(urls, strings.TrimRight(proxy, "/")+"/"+endpoint)
	}
	return urls
}

func parseReleasePayload(r io.Reader, channel string, isMihomoPre bool) (string, string, error) {
	var targetRelease githubRelease
	if channel == coreChannelTest && !isMihomoPre {
		var releases []githubRelease
		if err := json.NewDecoder(io.LimitReader(r, 2<<20)).Decode(&releases); err != nil {
			return "", "", fmt.Errorf("parse GitHub releases: %w", err)
		}
		for _, rel := range releases {
			if rel.Prerelease || coreChannel(rel.TagName) == coreChannelTest {
				targetRelease = rel
				break
			}
		}
		if targetRelease.TagName == "" && len(releases) > 0 {
			targetRelease = releases[0]
		}
	} else {
		if err := json.NewDecoder(io.LimitReader(r, 2<<20)).Decode(&targetRelease); err != nil {
			return "", "", fmt.Errorf("parse GitHub release: %w", err)
		}
	}

	v := normalizeCoreVersion(targetRelease.TagName)
	if v == "" || isCoreStaticReleaseTag(targetRelease.TagName) {
		for _, asset := range targetRelease.Assets {
			if strings.HasSuffix(strings.ToLower(asset.Name), ".zip") {
				if av := normalizeCoreVersion(asset.Name); av != "" {
					v = av
					break
				}
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

func fetchSingleRelease(ctx context.Context, client *http.Client, targetURL, channel string, isMihomoPre bool) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "zashdesktop")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("HTTP status %s", resp.Status)
	}

	return parseReleasePayload(resp.Body, channel, isMihomoPre)
}

// fetchLatestCoreRelease 并发请求官方及加速镜像源，首个成功者立即胜出并取消其他请求
func fetchLatestCoreRelease(owner, repository, channel string) (version string, sha256Digest string, err error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repository))

	isMihomoPre := (strings.EqualFold(owner, "MetaCubeX") || strings.EqualFold(owner, "vernesong")) && strings.EqualFold(repository, "mihomo") && channel == coreChannelTest
	if isMihomoPre {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), mihomoPrereleaseTag)
	} else if channel == coreChannelTest {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=3", url.PathEscape(owner), url.PathEscape(repository))
	}

	candidates := buildGitHubAPIURLs(endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resultCh := make(chan fetchReleaseResult, len(candidates))
	client := newCoreHTTPClient(10 * time.Second)

	for _, candidate := range candidates {
		go func(targetURL string) {
			v, digest, fetchErr := fetchSingleRelease(ctx, client, targetURL, channel, isMihomoPre)
			resultCh <- fetchReleaseResult{
				url:     targetURL,
				version: v,
				digest:  digest,
				err:     fetchErr,
			}
		}(candidate)
	}

	var lastErr error
	for i := 0; i < len(candidates); i++ {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", "", fmt.Errorf("check core update timed out: %w", lastErr)
			}
			return "", "", ctx.Err()
		case res := <-resultCh:
			if res.err == nil && res.version != "" {
				cancel()
				debugLogf("core", "latest release fetched from %s: version=%s digest=%s", res.url, res.version, res.digest)
				return res.version, res.digest, nil
			}
			lastErr = res.err
			debugLogf("core", "candidate %s check update failed: %v", res.url, res.err)
		}
	}

	if lastErr != nil {
		return "", "", fmt.Errorf("check core update failed: %w", lastErr)
	}
	return "", "", errors.New("check core update: all endpoints failed")
}

func findLatestReleaseForURL(downloadURLTemplate, channel string) (string, string, error) {
	owner, repository := parseGitHubRepo(downloadURLTemplate)
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

	owner, repository := parseGitHubRepo(downloadURL)

	var latest, digest string
	cachedHit := false
	if !force {
		if cached, ok := s.getCachedLatestRelease(owner, repository, config.Channel); ok {
			latest = cached.version
			digest = cached.digest
			cachedHit = true
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
	debugLogf("release", "check update result: type=%s current=%s latest=%s updateAvailable=%t cached=%t", coreType, config.Version, config.LatestVersion, config.UpdateAvailable, cachedHit)
	return s.applyCheckedConfig(config)
}
