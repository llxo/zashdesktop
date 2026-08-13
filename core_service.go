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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	coreExecutableBaseName = "sing-box"
	maxCoreDownload        = 200 << 20
	maxCoreBinary          = 100 << 20
	maxCoreConfig          = 20 << 20
	defaultCoreRunArgs     = "run -c config.json -D ."
)

var (
	coreTagPattern     = regexp.MustCompile(`(?i)^v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)$`)
	coreOutputPattern  = regexp.MustCompile(`(?i)(^|[^0-9a-z])v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)([^0-9a-z]|$)`)
	testChannelPattern = regexp.MustCompile(`(?i)(^|[-._])(alpha|beta|rc|dev|nightly|preview)([-._]|\d|$)`)
)

type CoreConfig struct {
	URLTemplate       string `json:"urlTemplate"`
	ConfiguredVersion string `json:"configuredVersion"`
	Version           string `json:"version"`
	VersionDetail     string `json:"versionDetail"`
	Channel           string `json:"channel"`
	CorePath          string `json:"corePath"`
	InstalledVersion  string `json:"installedVersion"`
	Installed         bool   `json:"installed"`
	LatestVersion     string `json:"latestVersion"`
	UpdateAvailable   bool   `json:"updateAvailable"`
	BackupVersion     string `json:"backupVersion"`
	BackupAvailable   bool   `json:"backupAvailable"`
	PendingVersion    string `json:"pendingVersion"`
	UpdatePending     bool   `json:"updatePending"`
	RunArgs           string `json:"runArgs"`
	ConfigURL         string `json:"configURL"`
	Running           bool   `json:"running"`
	PID               int    `json:"pid"`
	LogPath           string `json:"logPath"`
	ConfigPath        string `json:"configPath"`
	ConfigAvailable   bool   `json:"configAvailable"`
}

type CoreService struct {
	executableDir string
	mu            sync.Mutex
	process       *exec.Cmd
	processDone   chan struct{}
}

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
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

func (s *CoreService) ServiceShutdown() error {
	return s.stopCoreProcess()
}

func (s *CoreService) ServiceName() string {
	return "CoreService"
}

func (s *CoreService) GetConfig() (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
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
	config.LatestVersion = ""
	config.UpdateAvailable = false
	config.Version = existing.Version
	config.VersionDetail = existing.VersionDetail
	config.InstalledVersion = existing.InstalledVersion
	config.Installed = existing.Installed
	config.BackupVersion = existing.BackupVersion
	config.BackupAvailable = existing.BackupAvailable
	config.PendingVersion = existing.PendingVersion
	config.UpdatePending = existing.UpdatePending
	config.RunArgs = existing.RunArgs
	config.ConfigURL = existing.ConfigURL
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) DownloadConfig(rawURL string) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	rawURL = strings.TrimSpace(rawURL)
	if err := validateHTTPURL(rawURL, "配置下载地址"); err != nil {
		return CoreConfig{}, err
	}

	client := newCoreHTTPClient(5 * time.Minute)
	response, err := client.Get(rawURL)
	if err != nil {
		return CoreConfig{}, fmt.Errorf("download sing-box config: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return CoreConfig{}, fmt.Errorf("download sing-box config: server returned %s", response.Status)
	}
	if response.ContentLength > maxCoreConfig {
		return CoreConfig{}, errors.New("sing-box config is too large")
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxCoreConfig+1))
	if err != nil {
		return CoreConfig{}, fmt.Errorf("read sing-box config: %w", err)
	}
	if len(data) == 0 {
		return CoreConfig{}, errors.New("sing-box config is empty")
	}
	if len(data) > maxCoreConfig {
		return CoreConfig{}, errors.New("sing-box config is too large")
	}
	if err := os.MkdirAll(s.coreDir(), 0o755); err != nil {
		return CoreConfig{}, fmt.Errorf("create core directory: %w", err)
	}
	if err := writeFileAtomically(s.singboxConfigPath(), data, 0o600); err != nil {
		return CoreConfig{}, fmt.Errorf("write sing-box config: %w", err)
	}

	config.ConfigURL = rawURL
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
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

	targetVersion := normalizeCoreVersion(config.LatestVersion)
	if targetVersion == "" {
		targetVersion = normalizeCoreVersion(config.ConfiguredVersion)
	}
	if targetVersion == "" {
		targetVersion, err = findLatestReleaseForConfig(config)
		if err != nil {
			return CoreConfig{}, err
		}
	}
	downloadURL := strings.ReplaceAll(config.URLTemplate, "{version}", targetVersion)
	parsedURL, err := url.Parse(downloadURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return CoreConfig{}, errors.New("core download URL is invalid")
	}

	expectedSHA256 := ""
	if owner, repository, repositoryErr := githubRepository(config.URLTemplate); repositoryErr == nil {
		expectedSHA256, err = findReleaseAssetDigest(owner, repository, targetVersion, downloadURL)
		if err != nil {
			return CoreConfig{}, err
		}
	}

	archivePath, err := s.download(downloadURL, expectedSHA256)
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
		installedVersion, versionDetail, versionErr := readCoreVersionDetail(corePath)
		if versionErr != nil {
			return CoreConfig{}, versionErr
		}
		config.Version = installedVersion
		config.VersionDetail = versionDetail
		config.Channel = coreChannel(installedVersion)
		config.InstalledVersion = installedVersion
		config.Installed = true
		config.LatestVersion = targetVersion
		config.UpdateAvailable = compareCoreVersions(mustParseCoreVersion(targetVersion), mustParseCoreVersion(installedVersion)) > 0
		config.PendingVersion = ""
		config.UpdatePending = false
	} else {
		pendingVersion, _, versionErr := readCoreVersionDetail(s.pendingCorePath())
		if versionErr != nil {
			return CoreConfig{}, versionErr
		}
		config.LatestVersion = targetVersion
		config.PendingVersion = pendingVersion
		config.UpdateAvailable = true
		config.UpdatePending = true
	}
	s.applyBackupVersion(&config)
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
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
		if config.ConfiguredVersion == "" {
			return CoreConfig{}, err
		}
		config.LatestVersion = config.ConfiguredVersion
		config.UpdateAvailable = config.Version == "" || compareCoreVersions(mustParseCoreVersion(config.LatestVersion), mustParseCoreVersion(config.Version)) > 0
		if err := s.saveConfigLocked(config); err != nil {
			return CoreConfig{}, err
		}
		s.applyRuntimeState(&config)
		return config, nil
	}
	latest, err := findLatestRelease(owner, repository, config.Channel)
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
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) RollbackCore() (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	backupPath := s.backupCorePath()
	if !fileExists(backupPath) {
		return CoreConfig{}, errors.New("no previous sing-box core is available")
	}

	currentPath := s.corePath()
	temporaryPath := currentPath + ".rollback"
	_ = os.Remove(temporaryPath)
	if fileExists(currentPath) {
		if err := os.Rename(currentPath, temporaryPath); err != nil {
			if isFileLockedError(err) {
				return CoreConfig{}, errors.New("the current sing-box core is still running")
			}
			return CoreConfig{}, fmt.Errorf("prepare core rollback: %w", err)
		}
	}
	if err := os.Rename(backupPath, currentPath); err != nil {
		_ = os.Rename(temporaryPath, currentPath)
		return CoreConfig{}, fmt.Errorf("restore previous core: %w", err)
	}
	if fileExists(temporaryPath) {
		if err := os.Rename(temporaryPath, backupPath); err != nil {
			return CoreConfig{}, fmt.Errorf("save current core backup: %w", err)
		}
	}

	version, detail, err := readCoreVersionDetail(currentPath)
	if err != nil {
		return CoreConfig{}, err
	}
	config.Version = version
	config.VersionDetail = detail
	config.InstalledVersion = version
	config.Installed = true
	config.Channel = coreChannel(version)
	config.PendingVersion = ""
	config.UpdatePending = false
	config.UpdateAvailable = config.LatestVersion != "" && compareCoreVersions(mustParseCoreVersion(config.LatestVersion), mustParseCoreVersion(version)) > 0
	s.applyBackupVersion(&config)
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) SaveRunArgs(rawArgs string) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	config.RunArgs = strings.TrimSpace(rawArgs)
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) StartCore(rawArgs string) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.process != nil {
		return CoreConfig{}, errors.New("sing-box core is already running")
	}

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	if !fileExists(s.corePath()) {
		return CoreConfig{}, errors.New("sing-box core is not installed")
	}

	runArgs := strings.TrimSpace(rawArgs)
	if runArgs == "" {
		runArgs = strings.TrimSpace(config.RunArgs)
	}
	if runArgs == "" {
		runArgs = defaultCoreRunArgs
	}
	args, err := parseCoreCommandLine(runArgs)
	if err != nil {
		return CoreConfig{}, err
	}
	if len(args) == 0 {
		return CoreConfig{}, errors.New("请输入 sing-box 命令行参数")
	}

	if err := os.MkdirAll(s.coreDir(), 0o755); err != nil {
		return CoreConfig{}, fmt.Errorf("create core directory: %w", err)
	}
	logFile, err := os.OpenFile(s.coreLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return CoreConfig{}, fmt.Errorf("open core log: %w", err)
	}

	command := exec.Command(s.corePath(), args...)
	command.Dir = s.coreDir()
	command.Stdout = logFile
	command.Stderr = logFile
	configureCoreCommand(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return CoreConfig{}, fmt.Errorf("start sing-box core: %w", err)
	}

	config.RunArgs = runArgs
	if err := s.saveConfigLocked(config); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = logFile.Close()
		return CoreConfig{}, err
	}

	done := make(chan struct{})
	s.process = command
	s.processDone = done
	go s.waitForCore(command, logFile, done)

	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) StopCore() (CoreConfig, error) {
	if err := s.stopCoreProcess(); err != nil {
		return CoreConfig{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) RestartCore(rawArgs string) (CoreConfig, error) {
	if err := s.stopCoreProcess(); err != nil {
		return CoreConfig{}, err
	}
	return s.StartCore(rawArgs)
}

func (s *CoreService) stopCoreProcess() error {
	s.mu.Lock()
	process := s.process
	done := s.processDone
	s.mu.Unlock()
	if process == nil {
		return nil
	}

	select {
	case <-done:
		return nil
	default:
	}

	if err := process.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		select {
		case <-done:
			return nil
		default:
		}
		return fmt.Errorf("stop sing-box core: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("timed out waiting for sing-box core to stop")
	}
}

func (s *CoreService) waitForCore(command *exec.Cmd, logFile *os.File, done chan struct{}) {
	err := command.Wait()
	if err != nil {
		_, _ = fmt.Fprintf(logFile, "\n[sing-box exited: %v]\n", err)
	}
	_ = logFile.Close()

	s.mu.Lock()
	if s.process == command {
		s.process = nil
		s.processDone = nil
	}
	s.mu.Unlock()
	close(done)
}

func (s *CoreService) applyRuntimeState(config *CoreConfig) {
	config.Running = s.process != nil
	config.PID = 0
	config.LogPath = s.coreLogPath()
	config.ConfigPath = s.singboxConfigPath()
	config.ConfigAvailable = fileExists(config.ConfigPath)
	if config.RunArgs == "" {
		config.RunArgs = defaultCoreRunArgs
	}
	if config.Running {
		config.PID = s.process.Process.Pid
	}
}

func (s *CoreService) loadConfigLocked() (CoreConfig, error) {
	config := CoreConfig{CorePath: s.corePath()}
	data, err := os.ReadFile(s.configPath())
	if errors.Is(err, os.ErrNotExist) {
		s.applyCurrentVersion(&config, "")
		s.applyBackupVersion(&config)
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
	s.applyCurrentVersion(&config, "")
	if config.UpdatePending && config.PendingVersion != "" && fileExists(s.pendingCorePath()) {
		installed, err := s.installPendingCore()
		if err != nil {
			return CoreConfig{}, err
		}
		if installed {
			installedVersion, versionDetail, versionErr := readCoreVersionDetail(s.corePath())
			if versionErr != nil {
				return CoreConfig{}, versionErr
			}
			config.Version = installedVersion
			config.VersionDetail = versionDetail
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
	s.applyBackupVersion(&config)
	return config, nil
}

func (s *CoreService) saveConfigLocked(config CoreConfig) error {
	config.CorePath = s.corePath()
	config.Running = false
	config.PID = 0
	config.LogPath = ""
	config.ConfigPath = ""
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

func (s *CoreService) download(downloadURL, expectedSHA256 string) (string, error) {
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
	temporary, err := os.CreateTemp(s.executableDir, ".core-download-*"+suffix)
	if err != nil {
		return "", fmt.Errorf("create core archive: %w", err)
	}
	path := temporary.Name()
	defer func() {
		if temporary != nil {
			temporary.Close()
		}
	}()

	var writer io.Writer = temporary
	var digest = sha256.New()
	if expectedSHA256 != "" {
		writer = io.MultiWriter(temporary, digest)
	}
	written, err := io.Copy(writer, io.LimitReader(response.Body, maxCoreDownload+1))
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
	if expectedSHA256 != "" && !strings.EqualFold(fmt.Sprintf("%x", digest.Sum(nil)), expectedSHA256) {
		os.Remove(path)
		return "", errors.New("core archive checksum does not match the release digest")
	}
	temporary = nil
	return path, nil
}

func (s *CoreService) extractCore(archivePath, targetPath string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return false, fmt.Errorf("create core directory: %w", err)
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz") {
		return s.extractTarGZCore(archivePath, targetPath)
	}
	return s.extractZIPCore(archivePath, targetPath)
}

func (s *CoreService) extractZIPCore(archivePath, targetPath string) (bool, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return false, fmt.Errorf("open core ZIP archive: %w", err)
	}
	defer archive.Close()

	var selected *zip.File
	for _, entry := range archive.File {
		name := filepath.Base(strings.ReplaceAll(entry.Name, "\\", "/"))
		if !isCoreArchiveName(name) || entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.UncompressedSize64 > maxCoreBinary {
			continue
		}
		if selected == nil || strings.Count(entry.Name, "/") < strings.Count(selected.Name, "/") {
			selected = entry
		}
	}
	if selected == nil {
		return false, errors.New("sing-box executable was not found in the core ZIP archive")
	}

	reader, err := selected.Open()
	if err != nil {
		return false, fmt.Errorf("read core executable: %w", err)
	}
	defer reader.Close()
	return s.installExtractedCore(reader, targetPath)
}

func (s *CoreService) extractTarGZCore(archivePath, targetPath string) (bool, error) {
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
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return false, fmt.Errorf("read core TAR archive: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maxCoreBinary || !isCoreArchiveName(filepath.Base(header.Name)) {
			continue
		}
		return s.installExtractedCore(io.LimitReader(tarReader, header.Size), targetPath)
	}
	return false, errors.New("sing-box executable was not found in the core TAR.GZ archive")
}

func (s *CoreService) installExtractedCore(reader io.Reader, targetPath string) (bool, error) {
	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".sing-box-*"+filepath.Ext(targetPath))
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

func isCoreArchiveName(name string) bool {
	return strings.EqualFold(name, coreExecutableName()) || strings.EqualFold(name, coreExecutableBaseName)
}

func coreExecutableName() string {
	if runtime.GOOS == "windows" {
		return coreExecutableBaseName + ".exe"
	}
	return coreExecutableBaseName
}

func (s *CoreService) installPendingCore() (bool, error) {
	return s.replaceCoreExecutable(s.pendingCorePath(), s.corePath())
}

func (s *CoreService) replaceCoreExecutable(sourcePath, targetPath string) (bool, error) {
	backupPath := targetPath + ".bak"
	if fileExists(targetPath) {
		_ = os.Remove(backupPath)
		if err := os.Rename(targetPath, backupPath); err != nil {
			if isFileLockedError(err) {
				return false, nil
			}
			return false, fmt.Errorf("prepare core replacement: %w", err)
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		if fileExists(backupPath) {
			_ = os.Rename(backupPath, targetPath)
		}
		return false, fmt.Errorf("replace core executable: %w", err)
	}
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
	configuredVersion := ""
	if !strings.Contains(tag, "{version}") {
		configuredVersion = normalizeCoreVersion(tag)
		if configuredVersion == "" && !strings.EqualFold(tag, "latest") {
			return CoreConfig{}, errors.New("无法从地址识别版本号")
		}
		channel = coreChannel(configuredVersion)
		replacement := "{version}"
		if strings.HasPrefix(tag, "v") || strings.HasPrefix(tag, "V") {
			replacement = tag[:1] + "{version}"
		}
		parsedURL.Path = strings.Replace(parsedURL.Path, tag, replacement, 1)
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

func findLatestRelease(owner, repository, channel string) (string, error) {
	client := newCoreHTTPClient(30 * time.Second)
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(repository))
	if channel == "test" {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", url.PathEscape(owner), url.PathEscape(repository))
	}
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
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("check core update: GitHub returned %s", response.Status)
	}
	if channel == "test" {
		var releases []githubRelease
		if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&releases); err != nil {
			return "", fmt.Errorf("parse GitHub releases: %w", err)
		}
		for _, release := range releases {
			if !release.Prerelease {
				continue
			}
			if version := normalizeCoreVersion(release.TagName); version != "" {
				return version, nil
			}
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

func findLatestReleaseForConfig(config CoreConfig) (string, error) {
	owner, repository, err := githubRepository(config.URLTemplate)
	if err != nil {
		return "", err
	}
	return findLatestRelease(owner, repository, config.Channel)
}

func findReleaseAssetDigest(owner, repository, version, downloadURL string) (string, error) {
	client := newCoreHTTPClient(30 * time.Second)
	for _, tag := range []string{"v" + version, version} {
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(tag))
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "sing-box-gui")
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
		return "", nil
	}
	return "", nil
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
	return filepath.Join(s.coreDir(), coreExecutableName())
}

func (s *CoreService) coreLogPath() string {
	return filepath.Join(s.coreDir(), "sing-box.log")
}

func (s *CoreService) singboxConfigPath() string {
	return filepath.Join(s.coreDir(), "config.json")
}

func (s *CoreService) pendingCorePath() string {
	return filepath.Join(s.coreDir(), ".sing-box.pending"+filepath.Ext(s.corePath()))
}

func (s *CoreService) backupCorePath() string {
	return s.corePath() + ".bak"
}

func (s *CoreService) applyCurrentVersion(config *CoreConfig, supplied string) {
	version := normalizeCoreVersion(supplied)
	if version == "" && fileExists(s.corePath()) {
		version, _, _ = readCoreVersionDetail(s.corePath())
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

func validateHTTPURL(rawURL, label string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return fmt.Errorf("请输入有效的 HTTP(S) %s", label)
	}
	return nil
}

func parseCoreCommandLine(input string) ([]string, error) {
	var args []string
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	tokenStarted := false

	flush := func() {
		if !tokenStarted {
			return
		}
		args = append(args, current.String())
		current.Reset()
		tokenStarted = false
	}

	for index := 0; index < len(input); index++ {
		char := input[index]
		switch {
		case char == 0:
			return nil, errors.New("命令行参数包含无效字符")
		case char == '\\' && index+1 < len(input) && input[index+1] == '"' && !inSingleQuote:
			current.WriteByte('"')
			tokenStarted = true
			index++
		case char == '\\' && index+1 < len(input) && input[index+1] == '\'' && !inDoubleQuote:
			current.WriteByte('\'')
			tokenStarted = true
			index++
		case char == '"' && !inSingleQuote:
			inDoubleQuote = !inDoubleQuote
			tokenStarted = true
		case char == '\'' && !inDoubleQuote:
			inSingleQuote = !inSingleQuote
			tokenStarted = true
		case (char == ' ' || char == '\t' || char == '\r' || char == '\n') && !inSingleQuote && !inDoubleQuote:
			flush()
		default:
			current.WriteByte(char)
			tokenStarted = true
		}
	}

	if inSingleQuote || inDoubleQuote {
		return nil, errors.New("命令行参数包含未闭合的引号")
	}
	flush()
	return args, nil
}

func readCoreVersion(path string) (string, error) {
	version, _, err := readCoreVersionDetail(path)
	return version, err
}

func readCoreVersionDetail(corePath string) (string, string, error) {
	if !fileExists(corePath) {
		return "", "", errors.New("sing-box core is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, corePath, "version")
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

func (s *CoreService) applyBackupVersion(config *CoreConfig) {
	config.BackupAvailable = fileExists(s.backupCorePath())
	config.BackupVersion = ""
	if !config.BackupAvailable {
		return
	}
	config.BackupVersion, _, _ = readCoreVersionDetail(s.backupCorePath())
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
