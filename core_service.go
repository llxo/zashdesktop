package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	coreExecutableBaseName  = "sing-box"
	mihomoExecutableName    = "mihomo"
	coreTypeSingBox         = "sing-box"
	coreTypeMihomo          = "mihomo"
	coreChannelStable       = "stable"
	coreChannelTest         = "test"
	maxCoreDownload         = 200 << 20
	maxCoreBinary           = 100 << 20
	maxCoreConfig           = 20 << 20
	defaultCoreConfigFile   = "config.json"
	defaultMihomoConfigFile = "config.yaml"
	defaultCoreRunArgs      = "run -c config.json -D ."
	defaultMihomoRunArgs    = "-d . -f config.yaml"
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

const (
	mihomoPrereleaseTag     = "Prerelease-Alpha"
	mihomoTestURLTemplate   = "https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-{version}.zip"
	mihomoStableURLTemplate = "https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-v{version}.zip"
)

type CoreConfig struct {
	CoreType          string `json:"coreType"`
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
	RunArgs           string `json:"runArgs"`
	ConfigURL         string `json:"configURL"`
	ConfigFileName    string `json:"configFileName"`
	Running           bool   `json:"running"`
	PID               int    `json:"pid"`
	LogPath           string `json:"logPath"`
	ConfigPath        string `json:"configPath"`
	ConfigAvailable   bool   `json:"configAvailable"`
	RunAsAdmin        bool   `json:"runAsAdmin"`
	AutoStart         bool   `json:"autoStart"`
	AutoStartSingBox  bool   `json:"autoStartSingBox"`
	AutoStartMihomo   bool   `json:"autoStartMihomo"`
	BackendDebugLog   bool   `json:"backendDebugLog"`
	TrayAPIURL        string `json:"trayAPIURL"`
}

type sharedBehaviorConfig struct {
	RunAsAdmin       bool `json:"runAsAdmin"`
	AutoStart        bool `json:"autoStart"`
	AutoStartSingBox bool `json:"autoStartSingBox"`
	AutoStartMihomo  bool `json:"autoStartMihomo"`
	BackendDebugLog  bool `json:"backendDebugLog"`
}

type persistedCoreProfiles struct {
	ActiveCore string                `json:"activeCore"`
	Behavior   sharedBehaviorConfig  `json:"behavior"`
	Profiles   map[string]CoreConfig `json:"profiles"`
}

type coreVersionCacheItem struct {
	modTime time.Time
	size    int64
	version string
	detail  string
}

type CoreService struct {
	executableDir      string
	applicationPath    string
	operationMu        sync.Mutex
	mu                 sync.Mutex
	configGeneration   uint64
	startupCancel      context.CancelFunc
	startupDone        chan struct{}
	shuttingDown       bool
	process            *exec.Cmd
	processDone        chan struct{}
	processCoreType    string
	externalProcess    *os.Process
	externalCoreType   string
	stateLogged        bool
	lastRunning        bool
	lastPID            int
	trayAPIURL         string
	keepCoreOnShutdown bool
	onStateChange      func()

	cachedProfiles     *persistedCoreProfiles
	configModTime      time.Time
	versionCache       map[string]coreVersionCacheItem
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
	return &CoreService{
		executableDir:   filepath.Dir(executable),
		applicationPath: executable,
		versionCache:    make(map[string]coreVersionCacheItem),
	}, nil
}

var coreDebugLogState struct {
	sync.Mutex
	enabled bool
	file    *os.File
	logger  *log.Logger
}

func coreDebugf(format string, args ...any) {
	coreDebugLogState.Lock()
	defer coreDebugLogState.Unlock()
	if coreDebugLogState.enabled && coreDebugLogState.logger != nil {
		coreDebugLogState.logger.Printf("zashdesktop: core: "+format, args...)
	}
}

func configureCoreDebugLog(path string, enabled bool) error {
	coreDebugLogState.Lock()
	defer coreDebugLogState.Unlock()

	if !enabled {
		if coreDebugLogState.file != nil {
			_ = coreDebugLogState.file.Close()
		}
		coreDebugLogState.enabled = false
		coreDebugLogState.file = nil
		coreDebugLogState.logger = nil
		return nil
	}
	if coreDebugLogState.enabled && coreDebugLogState.file != nil {
		return nil
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open backend debug log: %w", err)
	}
	coreDebugLogState.file = file
	coreDebugLogState.logger = log.New(file, "", log.LstdFlags)
	coreDebugLogState.enabled = true
	return nil
}

func (s *CoreService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.executableDir = filepath.Dir(executable)
	s.applicationPath = executable
	startupContext, cancelStartup := context.WithCancel(ctx)
	startupDone := make(chan struct{})
	s.mu.Lock()
	if s.versionCache == nil {
		s.versionCache = make(map[string]coreVersionCacheItem)
	}
	s.startupCancel = cancelStartup
	s.startupDone = startupDone
	s.shuttingDown = false
	startupConfig, configErr := s.loadConfigLocked()
	if configErr == nil {
		s.trayAPIURL = startupConfig.TrayAPIURL
		if s.cachedProfiles != nil {
			s.syncSystemBehaviorOnce(&s.cachedProfiles.Behavior)
		}
	}
	s.mu.Unlock()
	if configErr == nil {
		if debugErr := configureCoreDebugLog(s.backendDebugLogPath(), startupConfig.BackendDebugLog); debugErr != nil {
			log.Printf("zashdesktop: configure backend debug log: %v", debugErr)
		}
	}
	coreDebugf("service startup: executable=%q directory=%q", s.applicationPath, s.executableDir)
	go func() {
		defer close(startupDone)
		s.operationMu.Lock()
		defer s.operationMu.Unlock()
		s.startCoreOnStartup(startupContext)
	}()
	return nil
}

func (s *CoreService) ServiceShutdown() error {
	coreDebugf("service shutdown")
	s.mu.Lock()
	s.shuttingDown = true
	cancelStartup := s.startupCancel
	startupDone := s.startupDone
	keepCore := s.keepCoreOnShutdown
	s.mu.Unlock()
	if cancelStartup != nil {
		cancelStartup()
	}
	if startupDone != nil {
		<-startupDone
	}
	s.mu.Lock()
	if s.startupDone == startupDone {
		s.startupCancel = nil
		s.startupDone = nil
	}
	s.mu.Unlock()
	if keepCore {
		coreDebugf("service shutdown: keeping managed core running")
		_ = configureCoreDebugLog("", false)
		return nil
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	err := s.stopManagedCoreProcess()
	if err != nil {
		coreDebugf("service shutdown: stop failed: %v", err)
	}
	_ = configureCoreDebugLog("", false)
	return err
}

func (s *CoreService) ServiceName() string {
	return "CoreService"
}

func (s *CoreService) keepCoreRunningOnShutdown() {
	s.mu.Lock()
	s.keepCoreOnShutdown = true
	s.mu.Unlock()
}

func (s *CoreService) setOnStateChange(cb func()) {
	s.mu.Lock()
	s.onStateChange = cb
	s.mu.Unlock()
}

func (s *CoreService) notifyStateChange() {
	s.mu.Lock()
	cb := s.onStateChange
	s.mu.Unlock()
	if cb != nil {
		go cb()
	}
}

func (s *CoreService) notifyStateChangeLocked() {
	if s.onStateChange != nil {
		cb := s.onStateChange
		go cb()
	}
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

func (s *CoreService) GetConfigForType(rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.loadConfigForTypeLocked(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) SaveURL(rawURL, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	existing, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, err := parseCoreURL(rawURL)
	if err != nil {
		return CoreConfig{}, err
	}
	if config.Channel == "" {
		config.Channel = existing.Channel
	}
	if config.Channel == "" {
		config.Channel = coreChannelStable
	}
	config.LatestVersion = ""
	config.UpdateAvailable = false
	config.Version = existing.Version
	config.VersionDetail = existing.VersionDetail
	config.InstalledVersion = existing.InstalledVersion
	config.Installed = existing.Installed
	config.RunArgs = existing.RunArgs
	config.CoreType = existing.CoreType
	config.ConfigURL = existing.ConfigURL
	config.AutoStartSingBox = existing.AutoStartSingBox
	config.AutoStartMihomo = existing.AutoStartMihomo
	config.RunAsAdmin = existing.RunAsAdmin
	config.AutoStart = existing.AutoStart
	config.BackendDebugLog = existing.BackendDebugLog
	config.TrayAPIURL = existing.TrayAPIURL
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) SaveChannel(rawChannel, rawCoreType string) (CoreConfig, error) {
	channel, err := normalizeCoreChannel(rawChannel)
	if err != nil {
		return CoreConfig{}, err
	}
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config.Channel = channel
	config.LatestVersion = ""
	config.UpdateAvailable = false
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
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

func (s *CoreService) DownloadConfig(rawURL, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
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
		return CoreConfig{}, fmt.Errorf("download %s config: %w", coreType, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return CoreConfig{}, fmt.Errorf("download %s config: server returned %s", coreType, response.Status)
	}
	if response.ContentLength > maxCoreConfig {
		return CoreConfig{}, fmt.Errorf("%s config is too large", coreType)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxCoreConfig+1))
	if err != nil {
		return CoreConfig{}, fmt.Errorf("read %s config: %w", coreType, err)
	}
	if len(data) == 0 {
		return CoreConfig{}, fmt.Errorf("%s config is empty", coreType)
	}
	if len(data) > maxCoreConfig {
		return CoreConfig{}, fmt.Errorf("%s config is too large", coreType)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while downloading; please retry")
	}
	if err := os.MkdirAll(s.coreDirFor(config.CoreType), 0o755); err != nil {
		return CoreConfig{}, fmt.Errorf("create core directory: %w", err)
	}
	if err := writeFileAtomically(s.configFilePath(config), data, 0o600); err != nil {
		return CoreConfig{}, fmt.Errorf("write %s config: %w", config.CoreType, err)
	}

	config.ConfigURL = rawURL
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) ImportConfig(rawContent, sourceFileName, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	if _, err := normalizeConfigFileName(sourceFileName, coreType); err != nil {
		return CoreConfig{}, err
	}
	data := []byte(rawContent)
	if len(data) == 0 {
		return CoreConfig{}, fmt.Errorf("%s config is empty", coreType)
	}
	if len(data) > maxCoreConfig {
		return CoreConfig{}, fmt.Errorf("%s config is too large", coreType)
	}

	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while importing; please retry")
	}
	if err := os.MkdirAll(s.coreDirFor(config.CoreType), 0o755); err != nil {
		return CoreConfig{}, fmt.Errorf("create core directory: %w", err)
	}
	if err := writeFileAtomically(s.configFilePath(config), data, 0o600); err != nil {
		return CoreConfig{}, fmt.Errorf("write %s config: %w", config.CoreType, err)
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
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

func (s *CoreService) CheckUpdate(currentVersion, rawCoreType string) (CoreConfig, error) {
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
	return s.saveCheckedConfig(config, generation)
}

func (s *CoreService) SaveRunArgs(rawArgs, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config.RunArgs = strings.TrimSpace(rawArgs)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) SaveConfigFileName(rawFileName, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	fileName, err := normalizeConfigFileName(rawFileName, coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config.ConfigFileName = fileName
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) SaveCoreType(rawCoreType string) (CoreConfig, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	if strings.TrimSpace(config.RunArgs) == "" || isDefaultCoreRunArgs(config.RunArgs) {
		config.RunArgs = defaultRunArgs(coreType)
	}
	config.CoreType = coreType
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) SaveBehavior(runAsAdmin, autoStart, autoStartSingBox, autoStartMihomo, backendDebugLog bool, rawTrayAPIURL, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	trayAPIURL, err := normalizeTrayAPIURL(rawTrayAPIURL)
	if err != nil {
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}

	if err := writeRunAsAdminSetting(s.applicationPath, runAsAdmin); err != nil {
		return CoreConfig{}, err
	}
	if err := writeAutoStartSetting(s.applicationPath, autoStart); err != nil {
		return CoreConfig{}, err
	}

	behavior := sharedBehaviorConfig{
		RunAsAdmin:       runAsAdmin,
		AutoStart:        autoStart,
		AutoStartSingBox: autoStartSingBox,
		AutoStartMihomo:  autoStartMihomo,
		BackendDebugLog:  backendDebugLog,
	}
	if behavior.AutoStartSingBox && behavior.AutoStartMihomo {
		if coreType == coreTypeMihomo {
			behavior.AutoStartSingBox = false
		} else {
			behavior.AutoStartMihomo = false
		}
	}
	applySharedBehavior(&config, behavior)
	config.TrayAPIURL = trayAPIURL
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveBehaviorLocked(config, behavior); err != nil {
		return CoreConfig{}, err
	}
	s.trayAPIURL = trayAPIURL
	if err := configureCoreDebugLog(s.backendDebugLogPath(), backendDebugLog); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) StartCore(rawArgs, rawCoreType string) (CoreConfig, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.startCore(rawArgs, rawCoreType)
}

func (s *CoreService) startCore(rawArgs, rawCoreType string) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	s.detectAnyExternalProcessLocked()
	if s.externalProcess != nil {
		return CoreConfig{}, fmt.Errorf("%s core is already running (PID %d)", s.externalCoreType, s.externalProcess.Pid)
	}
	if s.process != nil {
		alive, aliveErr := coreProcessAlive(s.process.Process)
		coreDebugf("start request: existing pid=%d alive=%t checkErr=%v", s.process.Process.Pid, alive, aliveErr)
		if aliveErr != nil {
			return CoreConfig{}, fmt.Errorf("check sing-box core status: %w", aliveErr)
		}
		if alive {
			runningCoreType := s.processCoreType
			if runningCoreType == "" {
				runningCoreType = coreTypeSingBox
			}
			return CoreConfig{}, fmt.Errorf("%s core is already running", runningCoreType)
		}
	}
	if s.process != nil && s.processDone == nil {
		return CoreConfig{}, errors.New("sing-box core is already running")
	}

	config, err := s.loadConfigForTypeLocked(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	if !fileExists(s.corePathFor(config.CoreType)) {
		return CoreConfig{}, errors.New("sing-box core is not installed")
	}

	runArgs := strings.TrimSpace(rawArgs)
	if runArgs == "" {
		runArgs = strings.TrimSpace(config.RunArgs)
	}
	if runArgs == "" {
		runArgs = defaultRunArgs(coreType)
	}
	args, err := parseCoreCommandLine(runArgs)
	if err != nil {
		return CoreConfig{}, err
	}
	if len(args) == 0 {
		return CoreConfig{}, errors.New("请输入 sing-box 命令行参数")
	}
	coreDebugf("start request accepted: type=%s path=%q args=%d config=%t", config.CoreType, s.corePathFor(config.CoreType), len(args), fileExists(s.configFilePath(config)))

	if err := os.MkdirAll(s.coreDirFor(config.CoreType), 0o755); err != nil {
		return CoreConfig{}, fmt.Errorf("create core directory: %w", err)
	}
	s.cleanCoreLogs(config.CoreType)
	logFile, err := os.OpenFile(s.logFilePath(config.CoreType), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return CoreConfig{}, fmt.Errorf("open core log: %w", err)
	}

	command := exec.Command(s.corePathFor(config.CoreType), args...)
	command.Dir = s.coreDirFor(config.CoreType)
	command.Stdout = logFile
	command.Stderr = logFile
	configureCoreCommand(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return CoreConfig{}, fmt.Errorf("start sing-box core: %w", err)
	}
	coreDebugf("process started: type=%s pid=%d", config.CoreType, command.Process.Pid)

	config.RunArgs = runArgs
	if err := s.saveConfigAndActivateLocked(config); err != nil {
		coreDebugf("process startup cleanup: save config failed: %v", err)
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = logFile.Close()
		return CoreConfig{}, err
	}

	done := make(chan struct{})
	s.process = command
	s.processDone = done
	s.processCoreType = coreType
	go s.waitForCore(command, logFile, done)

	s.applyRuntimeState(&config)
	s.notifyStateChangeLocked()
	return config, nil
}

func (s *CoreService) StopCore() (CoreConfig, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.stopCore()
}

func (s *CoreService) stopCore() (CoreConfig, error) {
	if err := s.stopCoreProcess(); err != nil {
		return CoreConfig{}, err
	}

	config, err := s.loadConfigLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyRuntimeState(&config)
	s.notifyStateChangeLocked()
	return config, nil
}

func (s *CoreService) RestartCore(rawArgs, rawCoreType string) (CoreConfig, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	s.mu.Lock()
	s.detectAnyExternalProcessLocked()
	managedProcessAlive := false
	if s.process != nil {
		alive, aliveErr := coreProcessAlive(s.process.Process)
		if aliveErr != nil {
			s.mu.Unlock()
			return CoreConfig{}, fmt.Errorf("check core status: %w", aliveErr)
		}
		managedProcessAlive = alive
	}
	if managedProcessAlive && s.processCoreType != coreType {
		runningCoreType := s.processCoreType
		if runningCoreType == "" {
			runningCoreType = coreTypeSingBox
		}
		s.mu.Unlock()
		return CoreConfig{}, fmt.Errorf("%s core is already running", runningCoreType)
	}
	if s.externalProcess != nil && s.externalCoreType != coreType {
		runningCoreType := s.externalCoreType
		s.mu.Unlock()
		return CoreConfig{}, fmt.Errorf("%s core is already running", runningCoreType)
	}
	s.mu.Unlock()
	if err := s.stopCoreProcess(); err != nil {
		return CoreConfig{}, err
	}
	return s.startCore(rawArgs, coreType)
}

func (s *CoreService) stopManagedCoreProcess() error {
	s.mu.Lock()
	process := s.process
	done := s.processDone
	s.mu.Unlock()
	if process == nil {
		coreDebugf("stop request: no managed core process")
		return nil
	}
	return s.stopManagedProcess(process, done)
}

func (s *CoreService) stopCoreProcess() error {
	s.mu.Lock()
	process := s.process
	done := s.processDone
	external := s.externalProcess
	s.mu.Unlock()
	if process == nil && external == nil {
		coreDebugf("stop request: no core process")
		return nil
	}
	if process != nil {
		return s.stopManagedProcess(process, done)
	}
	return s.stopExternalProcess(external)
}

func (s *CoreService) stopManagedProcess(process *exec.Cmd, done chan struct{}) error {
	if done == nil {
		return errors.New("sing-box core process state is invalid")
	}

	select {
	case <-done:
		coreDebugf("stop request: pid=%d already exited", process.Process.Pid)
		return nil
	default:
	}

	alive, statusErr := coreProcessAlive(process.Process)
	coreDebugf("stop request: pid=%d alive=%t checkErr=%v", process.Process.Pid, alive, statusErr)
	if statusErr == nil && !alive {
		<-done
		return nil
	}

	// Give the core a chance to flush state and close listeners before forcing it down.
	if err := requestCoreStop(process.Process); err != nil {
		coreDebugf("graceful stop signal failed: pid=%d err=%v", process.Process.Pid, err)
	} else {
		coreDebugf("graceful stop signal sent: pid=%d", process.Process.Pid)
	}
	graceTimer := time.NewTimer(5 * time.Second)
	select {
	case <-done:
		if !graceTimer.Stop() {
			select {
			case <-graceTimer.C:
			default:
			}
		}
		coreDebugf("core exited after graceful stop: pid=%d", process.Process.Pid)
		return nil
	case <-graceTimer.C:
	}

	coreDebugf("graceful stop timed out, forcing kill: pid=%d", process.Process.Pid)
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
		coreDebugf("core exited after force kill: pid=%d", process.Process.Pid)
		return nil
	case <-time.After(5 * time.Second):
		coreDebugf("core stop timed out: pid=%d", process.Process.Pid)
		return errors.New("timed out waiting for sing-box core to stop")
	}
}

func (s *CoreService) stopExternalProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	alive, statusErr := coreProcessAlive(process)
	coreDebugf("stop external request: pid=%d alive=%t checkErr=%v", process.Pid, alive, statusErr)
	if statusErr == nil && !alive {
		s.clearExternalProcess(process)
		return nil
	}
	if err := requestCoreStop(process); err != nil {
		coreDebugf("external graceful stop signal failed: pid=%d err=%v", process.Pid, err)
	} else {
		coreDebugf("external graceful stop signal sent: pid=%d", process.Pid)
	}
	if waitErr := waitForCoreProcessExit(process, 5*time.Second); waitErr == nil {
		coreDebugf("external core exited after graceful stop: pid=%d", process.Pid)
		s.clearExternalProcess(process)
		return nil
	}

	coreDebugf("external graceful stop timed out, forcing kill: pid=%d", process.Pid)
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop external sing-box core: %w", err)
	}
	if err := waitForCoreProcessExit(process, 5*time.Second); err != nil {
		coreDebugf("external core stop timed out: pid=%d", process.Pid)
		return err
	}
	s.clearExternalProcess(process)
	return nil
}

func waitForCoreProcessExit(process *os.Process, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := coreProcessAlive(process)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		select {
		case <-deadline.C:
			return errors.New("timed out waiting for external sing-box core to stop")
		case <-ticker.C:
		}
	}
}

func (s *CoreService) clearExternalProcess(process *os.Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.externalProcess == process {
		s.externalProcess = nil
		s.externalCoreType = ""
	}
}

func (s *CoreService) waitForCore(command *exec.Cmd, logFile *os.File, done chan struct{}) {
	err := command.Wait()
	if err == nil {
		coreDebugf("process exited: pid=%d path=%q", command.Process.Pid, command.Path)
	} else {
		coreDebugf("process exited with error: pid=%d path=%q err=%v", command.Process.Pid, command.Path, err)
	}
	if err != nil {
		_, _ = fmt.Fprintf(logFile, "\n[sing-box exited: %v]\n", err)
	}
	_ = logFile.Close()

	s.mu.Lock()
	if s.process == command {
		s.process = nil
		s.processDone = nil
		s.processCoreType = ""
	}
	s.mu.Unlock()
	close(done)
	s.notifyStateChange()
}

func (s *CoreService) applyRuntimeState(config *CoreConfig) {
	config.CoreType = normalizedCoreType(config.CoreType)
	s.detectExternalProcessLocked(config.CoreType)
	config.Running = false
	config.PID = 0
	config.LogPath = s.logFilePath(config.CoreType)
	config.ConfigPath = s.configFilePath(*config)
	config.ConfigAvailable = fileExists(config.ConfigPath)
	if config.RunArgs == "" {
		config.RunArgs = defaultRunArgs(config.CoreType)
	}
	if s.process != nil && s.processCoreType == config.CoreType {
		alive, err := coreProcessAlive(s.process.Process)
		if err != nil {
			coreDebugf("runtime status check failed: pid=%d err=%v", s.process.Process.Pid, err)
		}
		config.Running = err == nil && alive
	}
	if !config.Running && s.externalProcess != nil && s.externalCoreType == config.CoreType {
		alive, err := coreProcessAlive(s.externalProcess)
		if err != nil {
			coreDebugf("external runtime status check failed: pid=%d err=%v", s.externalProcess.Pid, err)
		} else if alive {
			config.Running = true
			config.PID = s.externalProcess.Pid
		} else {
			s.externalProcess = nil
			s.externalCoreType = ""
		}
	}
	if config.Running && s.process != nil && s.processCoreType == config.CoreType {
		config.PID = s.process.Process.Pid
	}
	if !s.stateLogged || s.lastRunning != config.Running || s.lastPID != config.PID {
		coreDebugf("runtime state changed: running=%t pid=%d type=%s", config.Running, config.PID, config.CoreType)
		s.stateLogged = true
		s.lastRunning = config.Running
		s.lastPID = config.PID
	}
}

func (s *CoreService) detectExternalProcessLocked(coreType string) {
	if s.process != nil {
		return
	}
	if s.externalProcess != nil {
		alive, err := coreProcessAlive(s.externalProcess)
		if err == nil && alive {
			return
		}
		coreDebugf("external core process no longer available: pid=%d err=%v", s.externalProcess.Pid, err)
		s.externalProcess = nil
		s.externalCoreType = ""
	}
	process, err := findExternalCoreProcess(coreType)
	if err != nil {
		coreDebugf("external core detection failed: type=%s err=%v", coreType, err)
		return
	}
	if process != nil {
		s.externalProcess = process
		s.externalCoreType = coreType
		coreDebugf("external core process detected: type=%s pid=%d", coreType, process.Pid)
	}
}

func (s *CoreService) detectAnyExternalProcessLocked() {
	if s.process != nil {
		return
	}
	s.detectExternalProcessLocked(coreTypeSingBox)
	if s.externalProcess == nil {
		s.detectExternalProcessLocked(coreTypeMihomo)
	}
}

func (s *CoreService) syncSystemBehaviorOnce(behavior *sharedBehaviorConfig) {
	if s.applicationPath == "" || behavior == nil {
		return
	}
	if runAsAdmin, err := readRunAsAdminSetting(s.applicationPath); err == nil {
		behavior.RunAsAdmin = runAsAdmin
	}
	if autoStart, err := readAutoStartSetting(); err == nil {
		behavior.AutoStart = autoStart
	}
}

func (s *CoreService) applySystemBehavior(config *CoreConfig) {}

func (s *CoreService) startCoreOnStartup(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	profiles, err := s.loadProfilesLocked()
	if err != nil {
		coreDebugf("startup check: loadErr=%v", err)
		return
	}
	coreType := ""
	if profiles.Behavior.AutoStartSingBox {
		coreType = coreTypeSingBox
	} else if profiles.Behavior.AutoStartMihomo {
		coreType = coreTypeMihomo
	}
	if coreType == "" {
		coreDebugf("startup check: no core configured for automatic startup")
		return
	}
	config, err := s.loadProfileFromStoreLocked(profiles, coreType)
	shouldStart := err == nil && fileExists(s.corePathFor(config.CoreType)) && fileExists(s.configFilePath(config))
	runArgs := config.RunArgs
	coreDebugf("startup check: loadErr=%v autoStartCoreType=%s coreInstalled=%t configAvailable=%t", err, coreType, fileExists(s.corePathFor(config.CoreType)), fileExists(s.configFilePath(config)))
	if !shouldStart || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	s.trayAPIURL = config.TrayAPIURL
	s.mu.Unlock()
	if _, err := s.startCore(runArgs, config.CoreType); err != nil {
		coreDebugf("start core on startup failed: %v", err)
	}
}

func (s *CoreService) loadConfigLocked() (CoreConfig, error) {
	profiles, err := s.loadProfilesLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	return s.loadProfileFromStoreLocked(profiles, normalizedCoreType(profiles.ActiveCore))
}

func (s *CoreService) loadConfigForTypeLocked(coreType string) (CoreConfig, error) {
	profiles, err := s.loadProfilesLocked()
	if err != nil {
		return CoreConfig{}, err
	}
	return s.loadProfileFromStoreLocked(profiles, normalizedCoreType(coreType))
}

func (s *CoreService) loadConfigSnapshot(coreType string) (CoreConfig, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation := s.configGeneration
	config, err := s.loadConfigForTypeLocked(coreType)
	return config, generation, err
}

func (s *CoreService) saveCheckedConfig(config CoreConfig, generation uint64) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while checking updates; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) loadProfileFromStoreLocked(profiles persistedCoreProfiles, coreType string) (CoreConfig, error) {
	config, ok := profiles.Profiles[coreType]
	if !ok {
		config = CoreConfig{}
	}
	config.CoreType = coreType
	configFileName, configFileNameErr := normalizeConfigFileName(config.ConfigFileName, coreType)
	if configFileNameErr != nil {
		configFileName = defaultConfigFileName(coreType)
	}
	config.ConfigFileName = configFileName
	if strings.TrimSpace(config.TrayAPIURL) == "" {
		config.TrayAPIURL = defaultTrayAPIURL
	}
	applySharedBehavior(&config, profiles.Behavior)
	s.applySystemBehavior(&config)
	config.CorePath = s.corePathFor(config.CoreType)
	config.Installed = fileExists(config.CorePath)
	s.applyCurrentVersion(&config, "")
	return config, nil
}

func (s *CoreService) loadProfilesLocked() (persistedCoreProfiles, error) {
	configPath := s.configPath()
	stat, statErr := os.Stat(configPath)
	if statErr == nil && s.cachedProfiles != nil && stat.ModTime().Equal(s.configModTime) {
		return *s.cachedProfiles, nil
	}

	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		profiles := persistedCoreProfiles{
			ActiveCore: coreTypeSingBox,
			Profiles:   make(map[string]CoreConfig),
		}
		s.cachedProfiles = &profiles
		s.configModTime = time.Time{}
		return profiles, nil
	}
	if err != nil {
		if s.cachedProfiles != nil {
			return *s.cachedProfiles, nil
		}
		return persistedCoreProfiles{}, fmt.Errorf("read core config: %w", err)
	}

	var profiles persistedCoreProfiles
	if err := json.Unmarshal(data, &profiles); err != nil {
		if s.cachedProfiles != nil {
			return *s.cachedProfiles, nil
		}
		return persistedCoreProfiles{}, fmt.Errorf("parse core config: %w", err)
	}
	if profiles.Profiles == nil {
		profiles.Profiles = make(map[string]CoreConfig)
	}
	if profiles.ActiveCore == "" {
		profiles.ActiveCore = coreTypeSingBox
	}
	profiles.ActiveCore = normalizedCoreType(profiles.ActiveCore)
	normalizeSharedBehavior(&profiles.Behavior, profiles.ActiveCore)

	s.cachedProfiles = &profiles
	if statErr == nil {
		s.configModTime = stat.ModTime()
	}
	return profiles, nil
}

func normalizeSharedBehavior(behavior *sharedBehaviorConfig, preferredCore string) {
	if !behavior.AutoStartSingBox || !behavior.AutoStartMihomo {
		return
	}
	behavior.AutoStartSingBox = normalizedCoreType(preferredCore) == coreTypeSingBox
	behavior.AutoStartMihomo = normalizedCoreType(preferredCore) == coreTypeMihomo
}

func applySharedBehavior(config *CoreConfig, behavior sharedBehaviorConfig) {
	config.RunAsAdmin = behavior.RunAsAdmin
	config.AutoStart = behavior.AutoStart
	config.AutoStartSingBox = behavior.AutoStartSingBox
	config.AutoStartMihomo = behavior.AutoStartMihomo
	config.BackendDebugLog = behavior.BackendDebugLog
}

func (s *CoreService) saveConfigLocked(config CoreConfig) error {
	return s.saveConfigLockedWithActiveCore(config, false)
}

func (s *CoreService) saveConfigAndActivateLocked(config CoreConfig) error {
	return s.saveConfigLockedWithActiveCore(config, true)
}

func (s *CoreService) saveConfigLockedWithActiveCore(config CoreConfig, activate bool) error {
	profiles, err := s.loadProfilesLocked()
	if err != nil {
		return err
	}
	config.CoreType = normalizedCoreType(config.CoreType)
	config.CorePath = s.corePathFor(config.CoreType)
	config.Running = false
	config.PID = 0
	config.LogPath = ""
	config.ConfigPath = ""
	if activate {
		profiles.ActiveCore = config.CoreType
	}
	profiles.Profiles[config.CoreType] = config
	return s.writeProfilesLocked(profiles)
}

func (s *CoreService) saveBehaviorLocked(config CoreConfig, behavior sharedBehaviorConfig) error {
	profiles, err := s.loadProfilesLocked()
	if err != nil {
		return err
	}
	config.CoreType = normalizedCoreType(config.CoreType)
	config.CorePath = s.corePathFor(config.CoreType)
	config.Running = false
	config.PID = 0
	config.LogPath = ""
	config.ConfigPath = ""
	profiles.Behavior = behavior
	profiles.Profiles[config.CoreType] = config
	return s.writeProfilesLocked(profiles)
}

func (s *CoreService) writeProfilesLocked(profiles persistedCoreProfiles) error {
	data, err := marshalPersistedCoreProfiles(profiles)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	configPath := s.configPath()
	if err := writeFileAtomically(configPath, data, 0o600); err != nil {
		return err
	}
	if stat, err := os.Stat(configPath); err == nil {
		s.configModTime = stat.ModTime()
	}
	s.cachedProfiles = &profiles
	s.configGeneration++
	return nil
}

func marshalPersistedCoreProfiles(profiles persistedCoreProfiles) ([]byte, error) {
	data, err := json.Marshal(profiles)
	if err != nil {
		return nil, err
	}
	var document struct {
		ActiveCore string                                `json:"activeCore"`
		Behavior   sharedBehaviorConfig                  `json:"behavior"`
		Profiles   map[string]map[string]json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	for _, profile := range document.Profiles {
		delete(profile, "runAsAdmin")
		delete(profile, "autoStart")
		delete(profile, "autoStartSingBox")
		delete(profile, "autoStartMihomo")
		delete(profile, "backendDebugLog")
	}
	return json.MarshalIndent(document, "", "  ")
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

func normalizeCoreType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", coreTypeSingBox:
		return coreTypeSingBox, nil
	case coreTypeMihomo:
		return coreTypeMihomo, nil
	default:
		return "", fmt.Errorf("unsupported core type %q", raw)
	}
}

func normalizedCoreType(raw string) string {
	coreType, err := normalizeCoreType(raw)
	if err != nil {
		return coreTypeSingBox
	}
	return coreType
}

func defaultConfigFileName(coreType string) string {
	if normalizedCoreType(coreType) == coreTypeMihomo {
		return defaultMihomoConfigFile
	}
	return defaultCoreConfigFile
}

func normalizeConfigFileName(rawFileName, coreType string) (string, error) {
	fileName := strings.TrimSpace(rawFileName)
	if fileName == "" {
		return "", errors.New("请输入配置文件名")
	}
	if fileName != filepath.Base(fileName) || strings.ContainsAny(fileName, `<>:"/\|?*`) {
		return "", errors.New("配置文件名不能包含路径或特殊字符")
	}
	for _, character := range fileName {
		if character < 0x20 {
			return "", errors.New("配置文件名不能包含控制字符")
		}
	}
	extension := strings.ToLower(filepath.Ext(fileName))
	if normalizedCoreType(coreType) == coreTypeMihomo {
		if extension != ".yaml" {
			return "", errors.New("mihomo 配置文件名必须以 .yaml 结尾")
		}
	} else if extension != ".json" {
		return "", errors.New("sing-box 配置文件名必须以 .json 结尾")
	}
	return fileName, nil
}

func defaultRunArgs(coreType string) string {
	if normalizedCoreType(coreType) == coreTypeMihomo {
		return defaultMihomoRunArgs
	}
	return defaultCoreRunArgs
}

func isDefaultCoreRunArgs(raw string) bool {
	runArgs := strings.TrimSpace(raw)
	return runArgs == defaultCoreRunArgs || runArgs == defaultMihomoRunArgs
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
	if channel == coreChannelTest {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", url.PathEscape(owner), url.PathEscape(repository))
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

func (s *CoreService) configPath() string {
	return filepath.Join(s.executableDir, "profiles.json")
}

func (s *CoreService) backendDebugLogPath() string {
	return filepath.Join(s.executableDir, "debug.log")
}

func (s *CoreService) coreDirFor(coreType string) string {
	directory := "sing-box"
	if normalizedCoreType(coreType) == coreTypeMihomo {
		directory = "mihomo"
	}
	return filepath.Join(s.executableDir, directory)
}

func (s *CoreService) corePathFor(coreType string) string {
	return filepath.Join(s.coreDirFor(coreType), coreExecutableNameFor(coreType))
}

func (s *CoreService) logFilePath(coreType string) string {
	return filepath.Join(s.coreDirFor(coreType), "core.log")
}

func (s *CoreService) configFilePath(config CoreConfig) string {
	fileName, err := normalizeConfigFileName(config.ConfigFileName, config.CoreType)
	if err != nil {
		fileName = defaultConfigFileName(config.CoreType)
	}
	return filepath.Join(s.coreDirFor(config.CoreType), fileName)
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

func cleanLogFile(path string) {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "" || cleanPath == "." {
		return
	}
	if err := os.Remove(cleanPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		if file, openErr := os.OpenFile(cleanPath, os.O_WRONLY|os.O_TRUNC, 0o600); openErr == nil {
			_ = file.Close()
		}
	}
}

func (s *CoreService) cleanCoreLogs(coreType string) {
	coreDir := s.coreDirFor(coreType)
	entries, err := os.ReadDir(coreDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".log") {
			cleanLogFile(filepath.Join(coreDir, entry.Name()))
		}
	}
}


