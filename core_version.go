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
	semverPattern         = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9a-zA-Z.-]+)?`)
	alphaPattern          = regexp.MustCompile(`alpha(?:-smart)?-[0-9a-zA-Z]+`)
	atomEntryTagPattern   = regexp.MustCompile(`/releases/tag/([^"/?#\s]+)`)
	platformSuffixPattern = regexp.MustCompile(`-(?:windows|linux|darwin|android|freebsd|openbsd)(?:-[0-9a-zA-Z_]+)*$`)
	githubTagNamePattern  = regexp.MustCompile(`"tag_name"\s*:\s*"([^"]+)"`)
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
				config.UpdateAvailable = isCoreUpdateAvailable(cached.version, config.Version, config.Channel, config.CoreType)
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
	version := parseCoreVersionOutput(string(output), coreType)
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
	if v := alphaPattern.FindString(value); v != "" {
		return v
	}
	value = platformSuffixPattern.ReplaceAllString(value, "")
	if v := semverPattern.FindString(value); v != "" {
		return v
	}
	return strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V")
}

func parseCoreVersionOutput(output, coreType string) string {
	line := strings.TrimSpace(strings.Split(output, "\n")[0])
	if normalizedCoreType(coreType) == coreTypeMihomo {
		if v := alphaPattern.FindString(line); v != "" {
			return v
		}
		if v := semverPattern.FindString(line); v != "" {
			return v
		}
	}
	if strings.HasPrefix(line, "sing-box version ") {
		return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(line[17:]), "v"), "V")
	}
	return normalizeCoreVersion(line)
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

func isCoreUpdateAvailable(latest, current, channel, coreType string) bool {
	latest = normalizeCoreVersion(latest)
	current = normalizeCoreVersion(current)
	if latest == "" {
		return false
	}
	if current == "" {
		return true
	}
	if strings.EqualFold(latest, current) {
		return false
	}

	channel = strings.ToLower(strings.TrimSpace(channel))
	isMihomo := normalizedCoreType(coreType) == coreTypeMihomo

	// mihomo 特定处理：测试版使用 alpha-<hash> 形式
	if isMihomo {
		if channel == coreChannelTest {
			return true
		}
		if strings.HasPrefix(strings.ToLower(current), "alpha") {
			return true
		}
		return compareVersions(latest, current) > 0
	}

	// sing-box 特定处理：全部遵循标准 SemVer
	if channel == coreChannelTest {
		if strings.Contains(latest, "-") && !strings.Contains(current, "-") {
			return true
		}
	} else if channel == coreChannelStable || channel == "" {
		if strings.Contains(current, "-") && !strings.Contains(latest, "-") {
			return true
		}
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

func fetchGitHubText(ctx context.Context, targetURL string, timeout time.Duration, maxBytes int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "zashdesktop")

	client := newCoreHTTPClient(timeout)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func fetchLatestReleaseByRedirect(ctx context.Context, owner, repository string) (string, error) {
	targetURL := fmt.Sprintf("https://github.com/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repository))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "zashdesktop")

	client := &http.Client{
		Timeout:   6 * time.Second,
		Transport: sharedCoreTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusMultipleChoices || resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("missing Location header in redirect")
	}

	m := atomEntryTagPattern.FindStringSubmatch(loc)
	tag := ""
	if len(m) > 1 {
		tag = m[1]
	} else {
		tag = loc[strings.LastIndex(loc, "/")+1:]
	}

	version := normalizeCoreVersion(tag)
	if version == "" {
		return "", fmt.Errorf("unable to normalize version from redirect tag %q", tag)
	}
	return version, nil
}

func fetchLatestReleaseByAtom(ctx context.Context, owner, repository string) (string, error) {
	targetURL := fmt.Sprintf("https://github.com/%s/%s/releases.atom", url.PathEscape(owner), url.PathEscape(repository))
	content, err := fetchGitHubText(ctx, targetURL, 6*time.Second, 64*1024)
	if err != nil {
		return "", err
	}

	m := atomEntryTagPattern.FindStringSubmatch(content)
	if len(m) < 2 {
		return "", errors.New("no tag found in releases.atom")
	}

	version := normalizeCoreVersion(m[1])
	if version == "" {
		return "", fmt.Errorf("unable to normalize version from atom tag %q", m[1])
	}
	return version, nil
}

func fetchMihomoPrereleaseAlpha(ctx context.Context, owner, repository string) (string, error) {
	targetURL := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/version.txt", url.PathEscape(owner), url.PathEscape(repository), mihomoPrereleaseTag)
	content, err := fetchGitHubText(ctx, targetURL, 6*time.Second, 1024)
	if err != nil {
		return "", err
	}

	version := normalizeCoreVersion(strings.TrimSpace(content))
	if version == "" {
		return "", fmt.Errorf("unable to normalize version from version.txt %q", content)
	}
	return version, nil
}

// fetchLatestCoreReleaseAPI 请求官方 GitHub API 作为兜底回退
func fetchLatestCoreReleaseAPI(ctx context.Context, owner, repository, channel string, isMihomoPre bool) (string, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repository))
	if isMihomoPre {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), mihomoPrereleaseTag)
	} else if channel == coreChannelTest {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=3", url.PathEscape(owner), url.PathEscape(repository))
	}

	content, err := fetchGitHubText(ctx, endpoint, 10*time.Second, 2*1024*1024)
	if err != nil {
		debugLogf("core", "official GitHub API %s check update failed: %v", endpoint, err)
		return "", err
	}

	var targetTag string

	if channel == coreChannelTest && !isMihomoPre {
		var list []struct {
			TagName    string `json:"tag_name"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.Unmarshal([]byte(content), &list); err == nil && len(list) > 0 {
			for _, rel := range list {
				if rel.Prerelease || coreChannel(rel.TagName) == coreChannelTest {
					targetTag = rel.TagName
					break
				}
			}
			if targetTag == "" {
				targetTag = list[0].TagName
			}
		}
	} else {
		var single struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal([]byte(content), &single); err == nil {
			targetTag = single.TagName
		}
	}

	if targetTag == "" {
		if m := githubTagNamePattern.FindStringSubmatch(content); len(m) > 1 {
			targetTag = m[1]
		}
	}

	if isMihomoPre {
		if v := alphaPattern.FindString(content); v != "" {
			debugLogf("core", "latest release fetched from official GitHub API %s (alphaPattern): version=%s", endpoint, v)
			targetTag = v
		}
	}

	version := normalizeCoreVersion(targetTag)
	if version == "" {
		return "", fmt.Errorf("unable to parse tag_name from GitHub API response")
	}

	debugLogf("core", "latest release fetched from official GitHub API %s: version=%s", endpoint, version)
	return version, nil
}

type coreReleaseChannelResult struct {
	name    string
	version string
	err     error
}

// fetchLatestCoreRelease 双通道并发竞速（Web极速通道与官方API通道），首个成功者立即胜出
func fetchLatestCoreRelease(owner, repository, channel string) (version string, sha256Digest string, err error) {
	isMihomoPre := (strings.EqualFold(owner, "MetaCubeX") || strings.EqualFold(owner, "vernesong")) && strings.EqualFold(repository, "mihomo") && channel == coreChannelTest

	debugLogf("core", "start checking latest release: %s/%s (%s) with concurrent dual-channel (web + official api)", owner, repository, channel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resultCh := make(chan coreReleaseChannelResult, 2)

	// 通道 1：Web 极速通道（302 / releases.atom / version.txt）
	go func() {
		debugLogf("core", "requesting web channel for %s/%s (%s)", owner, repository, channel)
		var v string
		var fetchErr error

		if isMihomoPre {
			v, fetchErr = fetchMihomoPrereleaseAlpha(ctx, owner, repository)
		} else if channel == coreChannelTest {
			v, fetchErr = fetchLatestReleaseByAtom(ctx, owner, repository)
		} else {
			v, fetchErr = fetchLatestReleaseByRedirect(ctx, owner, repository)
		}

		if fetchErr != nil {
			debugLogf("core", "web channel finished with error for %s/%s: %v", owner, repository, fetchErr)
		} else {
			debugLogf("core", "web channel finished successfully for %s/%s: version=%s", owner, repository, v)
		}
		resultCh <- coreReleaseChannelResult{name: "web", version: v, err: fetchErr}
	}()

	// 通道 2：官方 API 通道
	go func() {
		debugLogf("core", "requesting official api channel for %s/%s (%s)", owner, repository, channel)
		v, fetchErr := fetchLatestCoreReleaseAPI(ctx, owner, repository, channel, isMihomoPre)
		if fetchErr != nil {
			debugLogf("core", "official api channel finished with error for %s/%s: %v", owner, repository, fetchErr)
		} else {
			debugLogf("core", "official api channel finished successfully for %s/%s: version=%s", owner, repository, v)
		}
		resultCh <- coreReleaseChannelResult{name: "api", version: v, err: fetchErr}
	}()

	var lastErr error
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", "", fmt.Errorf("check core update timed out: %w", lastErr)
			}
			return "", "", ctx.Err()
		case res := <-resultCh:
			if res.err == nil && res.version != "" {
				cancel()
				debugLogf("core", "latest release fetched: %s/%s version=%s (winner: %s channel)", owner, repository, res.version, res.name)
				return res.version, "", nil
			}
			lastErr = res.err
		}
	}

	if lastErr != nil {
		return "", "", fmt.Errorf("check core update failed: %w", lastErr)
	}
	return "", "", errors.New("check core update: all channels failed")
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
	config.UpdateAvailable = isCoreUpdateAvailable(latest, config.Version, config.Channel, config.CoreType)
	debugLogf("release", "check update result: type=%s current=%s latest=%s updateAvailable=%t cached=%t", coreType, config.Version, config.LatestVersion, config.UpdateAvailable, cachedHit)
	return s.applyCheckedConfig(config)
}
