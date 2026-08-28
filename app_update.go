package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	return compareCoreVersions(mustParseCoreVersion(normLatest), mustParseCoreVersion(normCurrent)) > 0
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

func (s *CoreService) CheckAppUpdate() (AppUpdateInfo, error) {
	debugLogf("update", "checking app update from GitHub: owner=%s repo=%s currentVersion=%s", appGithubOwner, appGithubRepo, appVersion)
	client := newCoreHTTPClient(30 * time.Second)
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", appGithubOwner, appGithubRepo)
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		debugLogf("update", "create app update request failed: %v", err)
		return AppUpdateInfo{}, fmt.Errorf("check app update: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "zashdesktop")

	response, err := client.Do(request)
	if err != nil {
		debugLogf("update", "check app update HTTP request failed: %v", err)
		return AppUpdateInfo{}, fmt.Errorf("check app update: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		debugLogf("update", "check app update server returned status %s", response.Status)
		return AppUpdateInfo{}, fmt.Errorf("check app update: GitHub returned %s", response.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&release); err != nil {
		debugLogf("update", "decode app release JSON failed: %v", err)
		return AppUpdateInfo{}, fmt.Errorf("parse GitHub release: %w", err)
	}

	var binaryAsset *githubAsset
	for i := range release.Assets {
		asset := &release.Assets[i]
		lower := strings.ToLower(asset.Name)
		if strings.HasSuffix(lower, ".exe") && !strings.HasSuffix(lower, ".sha256") && strings.Contains(lower, "windows") && strings.Contains(lower, "amd64") {
			binaryAsset = asset
			break
		}
	}

	if binaryAsset == nil {
		debugLogf("update", "no windows amd64 binary asset found in release %s", release.TagName)
		return AppUpdateInfo{}, errors.New("GitHub release 中未找到 Windows x64 可执行文件")
	}

	info := AppUpdateInfo{
		CurrentVersion:  appVersion,
		LatestVersion:   release.TagName,
		UpdateAvailable: isAppUpdateAvailable(appVersion, release.TagName),
		ReleaseURL:      release.HTMLURL,
		ReleaseNotes:    release.Body,
		PublishedAt:     release.PublishedAt,
		DownloadURL:     binaryAsset.BrowserDownloadURL,
		AssetSize:       binaryAsset.Size,
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
		return errors.New("更新正在进行中，请稍候")
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
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", appGithubOwner, appGithubRepo)
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		debugLogf("update", "create app release request failed: %v", err)
		return fmt.Errorf("lookup app release: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "zashdesktop")

	response, err := client.Do(request)
	if err != nil {
		debugLogf("update", "lookup app release HTTP request failed: %v", err)
		return fmt.Errorf("lookup app release: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		debugLogf("update", "lookup app release server returned %s", response.Status)
		return fmt.Errorf("lookup app release: GitHub returned %s", response.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&release); err != nil {
		debugLogf("update", "parse app release JSON failed: %v", err)
		return fmt.Errorf("parse GitHub release: %w", err)
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
		debugLogf("update", "install app update failed: no windows amd64 binary found in release %s", release.TagName)
		return errors.New("GitHub release 中未找到 Windows x64 可执行文件")
	}

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

	executable, err := os.Executable()
	if err != nil {
		debugLogf("update", "locate executable failed: %v", err)
		return fmt.Errorf("无法定位程序路径: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		debugLogf("update", "eval symlinks failed: %v", err)
		return fmt.Errorf("解析程序路径失败: %w", err)
	}
	exeDir := filepath.Dir(executable)
	exeBase := filepath.Base(executable)

	// Download binary to temp file in the same directory
	tempFile, err := os.CreateTemp(exeDir, fmt.Sprintf(".%s-update-*.tmp", exeBase))
	if err != nil {
		debugLogf("update", "create temp file in %q failed: %v", exeDir, err)
		return fmt.Errorf("创建临时更新文件失败（请确认是否具有写入权限）: %w", err)
	}
	tempFilePath := tempFile.Name()
	keepTempFile := false
	defer func() {
		if !keepTempFile {
			_ = tempFile.Close()
			_ = os.Remove(tempFilePath)
		}
	}()

	debugLogf("update", "downloading update binary to %s", tempFilePath)
	downloadClient := newCoreHTTPClient(10 * time.Minute)
	downloadReq, err := http.NewRequest(http.MethodGet, binaryAsset.BrowserDownloadURL, nil)
	if err != nil {
		debugLogf("update", "create download request failed: %v", err)
		return fmt.Errorf("创建下载请求失败: %w", err)
	}
	downloadReq.Header.Set("User-Agent", "zashdesktop")

	downloadResp, err := downloadClient.Do(downloadReq)
	if err != nil {
		debugLogf("update", "download update binary failed: %v", err)
		return fmt.Errorf("下载安装包失败: %w", err)
	}
	defer downloadResp.Body.Close()

	if downloadResp.StatusCode < http.StatusOK || downloadResp.StatusCode >= http.StatusMultipleChoices {
		debugLogf("update", "download update binary server returned status %s", downloadResp.Status)
		return fmt.Errorf("下载安装包失败: 服务器返回 %s", downloadResp.Status)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(tempFile, hasher)

	n, err := io.Copy(writer, io.LimitReader(downloadResp.Body, maxAppBinaryDownload))
	if err != nil {
		debugLogf("update", "save downloaded binary failed: %v", err)
		return fmt.Errorf("写入更新文件失败: %w", err)
	}
	if n == 0 {
		debugLogf("update", "downloaded binary is empty")
		return errors.New("下载的更新文件为空")
	}

	if err := tempFile.Close(); err != nil {
		debugLogf("update", "close temp update file failed: %v", err)
		return fmt.Errorf("保存更新文件失败: %w", err)
	}

	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if expectedSHA != "" && !strings.EqualFold(actualSHA, expectedSHA) {
		debugLogf("update", "SHA256 checksum mismatch: expected=%s actual=%s", expectedSHA, actualSHA)
		return fmt.Errorf("SHA256 校验失败: 期望 %s, 实际 %s", expectedSHA, actualSHA)
	}
	debugLogf("update", "binary downloaded and verified successfully (bytes=%d, sha256=%s)", n, actualSHA)

	// Rename current exe to old
	oldExePath := filepath.Join(exeDir, fmt.Sprintf("%s.old", exeBase))
	_ = os.Remove(oldExePath)

	if err := os.Rename(executable, oldExePath); err != nil {
		debugLogf("update", "rename %q to %q failed: %v", executable, oldExePath, err)
		return fmt.Errorf("备份当前可执行文件失败: %w", err)
	}

	// Rename temp file to target executable
	if err := os.Rename(tempFilePath, executable); err != nil {
		debugLogf("update", "rename %q to %q failed: %v, rolling back", tempFilePath, executable, err)
		_ = os.Rename(oldExePath, executable) // rollback
		return fmt.Errorf("替换程序文件失败: %w", err)
	}
	keepTempFile = true
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
