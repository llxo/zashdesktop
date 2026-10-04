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
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	appGithubOwner       = "llxo"
	appGithubRepo        = "zashdesktop"
	maxAppBinaryDownload = 300 << 20
)

type AppUpdateInfo struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	ReleaseURL      string `json:"releaseURL"`
	ReleaseNotes    string `json:"releaseNotes"`
	PublishedAt     string `json:"publishedAt"`
	DownloadURL     string `json:"downloadURL"`
	AssetSize       int64  `json:"assetSize"`
}

func normalizeAppVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return v
}

func isAppUpdateAvailable(currentVer, latestVer string) bool {
	normCurrent := normalizeAppVersion(currentVer)
	normLatest := normalizeAppVersion(latestVer)
	if normLatest == "" {
		return false
	}
	if normCurrent == "" || normCurrent == "0.0.0" {
		return true
	}
	return compareVersions(normLatest, normCurrent) > 0
}

func (s *CoreService) GetAppVersion() string {
	return appVersion
}

func (s *CoreService) GetAppUpdateInfo() (AppUpdateInfo, error) {
	s.mu.Lock()
	cached := s.cachedAppUpdate
	s.mu.Unlock()
	if cached.LatestVersion != "" {
		return cached, nil
	}
	return s.CheckAppUpdate()
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	HTMLURL     string        `json:"html_url"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type appReleaseTarget struct {
	release     githubRelease
	binaryAsset *githubAsset
	sha256Asset *githubAsset
}

func fetchLatestAppTagByRedirect(ctx context.Context, owner, repo string) (string, error) {
	targetURL := fmt.Sprintf("https://github.com/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repo))
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
		cleanLoc := strings.TrimRight(strings.Split(loc, "?")[0], "/")
		tag = cleanLoc[strings.LastIndex(cleanLoc, "/")+1:]
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", fmt.Errorf("unable to extract tag from redirect %q", loc)
	}
	return tag, nil
}

func fetchLatestAppRelease(ctx context.Context, client *http.Client) (appReleaseTarget, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", appGithubOwner, appGithubRepo)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		debugLogf("update", "create app release request failed: %v", err)
		return appReleaseTarget{}, fmt.Errorf("lookup app release: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "zashdesktop")

	response, err := client.Do(request)
	if err != nil {
		debugLogf("update", "lookup app release HTTP request failed: %v", err)
		return appReleaseTarget{}, fmt.Errorf("lookup app release: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		debugLogf("update", "lookup app release server returned %s", response.Status)
		return appReleaseTarget{}, fmt.Errorf("lookup app release: GitHub returned %s", response.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&release); err != nil {
		debugLogf("update", "parse app release JSON failed: %v", err)
		return appReleaseTarget{}, fmt.Errorf("parse GitHub release: %w", err)
	}

	var binaryAsset *githubAsset
	var sha256Asset *githubAsset
	for i := range release.Assets {
		asset := &release.Assets[i]
		lower := strings.ToLower(asset.Name)
		if strings.HasSuffix(lower, ".exe") && !strings.HasSuffix(lower, ".sha256") && strings.Contains(lower, "windows") && strings.Contains(lower, "amd64") {
			binaryAsset = asset
		} else if strings.HasSuffix(lower, ".sha256") && strings.Contains(lower, "windows") && strings.Contains(lower, "amd64") {
			sha256Asset = asset
		}
	}

	if binaryAsset == nil {
		debugLogf("update", "no windows amd64 binary asset found in release %s", release.TagName)
		return appReleaseTarget{}, errors.New("windows x64 executable not found in GitHub release")
	}

	return appReleaseTarget{
		release:     release,
		binaryAsset: binaryAsset,
		sha256Asset: sha256Asset,
	}, nil
}

func (s *CoreService) CheckAppUpdate() (AppUpdateInfo, error) {
	debugLogf("update", "start checking app update: current=%s owner=%s repo=%s (concurrent dual-channel)", appVersion, appGithubOwner, appGithubRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type appCheckResult struct {
		isTarget bool
		target   appReleaseTarget
		fastTag  string
		err      error
	}

	resultCh := make(chan appCheckResult, 2)
	client := newCoreHTTPClient(10 * time.Second)

	// 通道 1：官方 API 完整 release 通道
	go func() {
		debugLogf("update", "requesting official api channel for app update")
		target, err := fetchLatestAppRelease(ctx, client)
		if err != nil {
			debugLogf("update", "official api channel finished with error: %v", err)
		} else {
			debugLogf("update", "official api channel finished successfully: tag=%s", target.release.TagName)
		}
		resultCh <- appCheckResult{isTarget: true, target: target, err: err}
	}()

	// 通道 2：Web 302 极速 Tag 检测通道
	go func() {
		debugLogf("update", "requesting fast 302 redirect channel for app update")
		tag, err := fetchLatestAppTagByRedirect(ctx, appGithubOwner, appGithubRepo)
		if err != nil {
			debugLogf("update", "fast 302 redirect channel finished with error: %v", err)
		} else {
			debugLogf("update", "fast 302 redirect channel finished successfully: tag=%s", tag)
		}
		resultCh <- appCheckResult{isTarget: false, fastTag: tag, err: err}
	}()

	var (
		lastErr     error
		foundTarget bool
		finalTarget appReleaseTarget
		fastTag     string
	)

	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return AppUpdateInfo{}, fmt.Errorf("check app update timed out: %w", lastErr)
			}
			return AppUpdateInfo{}, ctx.Err()
		case res := <-resultCh:
			if res.err == nil {
				if res.isTarget {
					cancel()
					finalTarget = res.target
					foundTarget = true
					debugLogf("update", "app update fetched by official API channel (winner): latest=%s", finalTarget.release.TagName)
					break
				} else {
					fastTag = res.fastTag
					// 若通过 302 确认当前已是最新版，立即秒级胜出！
					if !isAppUpdateAvailable(appVersion, fastTag) {
						cancel()
						debugLogf("update", "app is already up-to-date by fast 302 channel (winner): current=%s latest=%s", appVersion, fastTag)
						info := AppUpdateInfo{
							CurrentVersion:  appVersion,
							LatestVersion:   fastTag,
							UpdateAvailable: false,
						}
						s.mu.Lock()
						s.cachedAppUpdate = info
						s.mu.Unlock()
						return info, nil
					}
					debugLogf("update", "new app version detected via fast 302 (%s > %s), awaiting full release metadata from API", fastTag, appVersion)
				}
			} else {
				lastErr = res.err
			}
		}
		if foundTarget {
			break
		}
	}

	if !foundTarget {
		if fastTag != "" && isAppUpdateAvailable(appVersion, fastTag) {
			debugLogf("update", "official API channel failed (%v), fallback to fast 302 tag: %s", lastErr, fastTag)
			info := AppUpdateInfo{
				CurrentVersion:  appVersion,
				LatestVersion:   fastTag,
				UpdateAvailable: true,
				ReleaseURL:      fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", appGithubOwner, appGithubRepo, fastTag),
			}
			s.mu.Lock()
			s.cachedAppUpdate = info
			s.mu.Unlock()
			return info, nil
		}
		if lastErr != nil {
			return AppUpdateInfo{}, fmt.Errorf("check app update failed: %w", lastErr)
		}
		return AppUpdateInfo{}, errors.New("check app update: all channels failed")
	}

	info := AppUpdateInfo{
		CurrentVersion:  appVersion,
		LatestVersion:   finalTarget.release.TagName,
		UpdateAvailable: isAppUpdateAvailable(appVersion, finalTarget.release.TagName),
		ReleaseURL:      finalTarget.release.HTMLURL,
		ReleaseNotes:    finalTarget.release.Body,
		PublishedAt:     finalTarget.release.PublishedAt,
		DownloadURL:     finalTarget.binaryAsset.BrowserDownloadURL,
		AssetSize:       finalTarget.binaryAsset.Size,
	}

	s.mu.Lock()
	s.cachedAppUpdate = info
	s.mu.Unlock()

	debugLogf("update", "check app update success: current=%s latest=%s available=%t downloadURL=%s", info.CurrentVersion, info.LatestVersion, info.UpdateAvailable, info.DownloadURL)
	return info, nil
}

func (s *CoreService) InstallAppUpdate() error {
	s.appUpdateMu.Lock()
	if s.isUpdatingApp {
		s.appUpdateMu.Unlock()
		debugLogf("update", "install app update skipped: update already in progress")
		return errors.New("update in progress, please wait")
	}
	s.isUpdatingApp = true
	s.appUpdateMu.Unlock()

	defer func() {
		s.appUpdateMu.Lock()
		s.isUpdatingApp = false
		s.appUpdateMu.Unlock()
	}()

	debugLogf("update", "starting app update installation")
	client := newCoreHTTPClient(30 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	target, err := fetchLatestAppRelease(ctx, client)
	if err != nil {
		return err
	}

	binaryAsset := target.binaryAsset
	sha256Asset := target.sha256Asset

	expectedSHA := ""
	if strings.HasPrefix(strings.ToLower(binaryAsset.Digest), "sha256:") {
		expectedSHA = strings.TrimSpace(binaryAsset.Digest[len("sha256:"):])
	} else if sha256Asset != nil {
		shaReq, err := http.NewRequest(http.MethodGet, sha256Asset.BrowserDownloadURL, nil)
		if err == nil {
			shaReq.Header.Set("User-Agent", "zashdesktop")
			shaResp, err := client.Do(shaReq)
			if err == nil && shaResp.StatusCode == http.StatusOK {
				data, _ := io.ReadAll(io.LimitReader(shaResp.Body, 64<<10))
				shaResp.Body.Close()
				fields := strings.Fields(string(data))
				if len(fields) > 0 {
					expectedSHA = strings.TrimSpace(fields[0])
				}
			} else if shaResp != nil {
				shaResp.Body.Close()
			}
		}
	}
	debugLogf("update", "target binary: name=%s url=%s expectedSHA=%s", binaryAsset.Name, binaryAsset.BrowserDownloadURL, expectedSHA)

	executable, exeDir, err := executablePathAndDir()
	if err != nil {
		debugLogf("update", "locate executable failed: %v", err)
		return fmt.Errorf("unable to locate application path: %w", err)
	}
	exeBase := filepath.Base(executable)

	tempFilePath, err := downloadFile(binaryAsset.BrowserDownloadURL, exeDir, DownloadOptions{
		ExpectedSHA256: expectedSHA,
		MaxBytes:       maxAppBinaryDownload,
		FilePattern:    fmt.Sprintf(".%s-update-*.tmp", exeBase),
		UseGitHubProxy: s.isGitHubProxyEnabled(),
	})
	if err != nil {
		debugLogf("update", "download update binary failed: %v", err)
		return fmt.Errorf("failed to download update package: %w", err)
	}
	defer os.Remove(tempFilePath)

	// Rename current exe to old
	oldExePath := filepath.Join(exeDir, fmt.Sprintf("%s.old", exeBase))
	_ = os.Remove(oldExePath)

	if err := os.Rename(executable, oldExePath); err != nil {
		debugLogf("update", "rename %q to %q failed: %v", executable, oldExePath, err)
		return fmt.Errorf("failed to backup current executable: %w", err)
	}

	// Rename temp file to target executable
	if err := os.Rename(tempFilePath, executable); err != nil {
		debugLogf("update", "rename %q to %q failed: %v, rolling back", tempFilePath, executable, err)
		_ = os.Rename(oldExePath, executable) // rollback
		return fmt.Errorf("failed to replace executable file: %w", err)
	}
	debugLogf("update", "executable successfully replaced: %s", executable)

	// Launch background helper to restart app after current process exits
	pid := os.Getpid()
	args := os.Args[1:]
	var argList string
	if len(args) > 0 {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(a, "'", "''"))
		}
		argList = "-ArgumentList " + strings.Join(quoted, ", ")
	}

	psScript := fmt.Sprintf(
		"$p = Get-Process -Id %d -ErrorAction SilentlyContinue; while ($p -and -not $p.HasExited) { Start-Sleep -Milliseconds 100; $p = Get-Process -Id %d -ErrorAction SilentlyContinue }; Start-Process -FilePath '%s' %s",
		pid,
		pid,
		strings.ReplaceAll(executable, "'", "''"),
		argList,
	)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", psScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	if err := cmd.Start(); err != nil {
		debugLogf("update", "launch PowerShell restart helper failed: %v, attempting CMD fallback", err)
		cmdFallback := exec.Command("cmd.exe", "/c", fmt.Sprintf("ping 127.0.0.1 -n 2 >nul & start \"\" \"%s\"", executable))
		cmdFallback.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000,
		}
		_ = cmdFallback.Start()
	} else {
		debugLogf("update", "PowerShell restart helper launched successfully")
	}

	go func() {
		time.Sleep(500 * time.Millisecond)
		s.mu.Lock()
		app := s.app
		s.mu.Unlock()
		debugLogf("update", "quitting application for update restart")
		if app != nil {
			app.Quit()
		} else {
			os.Exit(0)
		}
	}()

	return nil
}
