package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
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
	CoreLogError      bool   `json:"coreLogError"`
	ConfigPath        string `json:"configPath"`
	ConfigAvailable   bool   `json:"configAvailable"`
	RunAsAdmin        bool   `json:"runAsAdmin"`
	IsAdmin           bool   `json:"isAdmin"`
	AutoStart         bool   `json:"autoStart"`
	AutoStartSingBox  bool   `json:"autoStartSingBox"`
	AutoStartMihomo   bool   `json:"autoStartMihomo"`
	BackendDebugLog   bool   `json:"backendDebugLog"`
	StopCoreOnExit    bool   `json:"stopCoreOnExit"`
	TrayAPIURL        string `json:"trayAPIURL"`
}

type sharedBehaviorConfig struct {
	RunAsAdmin       bool  `json:"runAsAdmin"`
	AutoStart        bool  `json:"autoStart"`
	AutoStartSingBox bool  `json:"autoStartSingBox"`
	AutoStartMihomo  bool  `json:"autoStartMihomo"`
	BackendDebugLog  bool  `json:"backendDebugLog"`
	StopCoreOnExit   *bool `json:"stopCoreOnExit,omitempty"`
}

func (b sharedBehaviorConfig) shouldStopCoreOnExit() bool {
	if b.StopCoreOnExit != nil {
		return *b.StopCoreOnExit
	}
	return true
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
	stoppingPids       map[int]bool
	coreLogError       map[string]bool

	app             *application.App
	appUpdateMu     sync.Mutex
	cachedAppUpdate AppUpdateInfo
	isUpdatingApp   bool

	lastDeletedFiles map[string]deletedConfigFile

	cachedProfiles     *persistedCoreProfiles
	configModTime      time.Time
	versionCacheMu     sync.RWMutex
	versionCache       map[string]coreVersionCacheItem
	remoteReleaseMu    sync.Mutex
	remoteReleaseCache map[string]remoteReleaseCacheItem
}

func NewCoreService() (*CoreService, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	execDir := filepath.Dir(executable)
	service := &CoreService{
		executableDir:      execDir,
		applicationPath:    executable,
		stoppingPids:       make(map[int]bool),
		coreLogError:       make(map[string]bool),
		lastDeletedFiles:   make(map[string]deletedConfigFile),
		versionCache:       make(map[string]coreVersionCacheItem),
		remoteReleaseCache: make(map[string]remoteReleaseCacheItem),
	}
	if profiles, err := service.loadProfilesLocked(); err == nil {
		if profiles.Behavior.BackendDebugLog {
			_ = configureCoreDebugLog(service.backendDebugLogPath(), true)
		}
	}
	return service, nil
}

var coreDebugLogState struct {
	sync.Mutex
	enabled atomic.Bool
	file    *os.File
	logger  *log.Logger
}

func debugLogf(module, format string, args ...any) {
	if !coreDebugLogState.enabled.Load() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	var line string
	if module != "" {
		line = fmt.Sprintf("zashdesktop: [%s] %s", module, msg)
	} else {
		line = fmt.Sprintf("zashdesktop: %s", msg)
	}
	coreDebugLogState.Lock()
	if coreDebugLogState.logger != nil {
		_ = coreDebugLogState.logger.Output(2, line)
	}
	coreDebugLogState.Unlock()
}

func coreDebugf(format string, args ...any) {
	debugLogf("core", format, args...)
}

func configureCoreDebugLog(path string, enabled bool) error {
	coreDebugLogState.Lock()
	defer coreDebugLogState.Unlock()

	if !enabled {
		if coreDebugLogState.file != nil {
			_ = coreDebugLogState.file.Close()
		}
		coreDebugLogState.enabled.Store(false)
		coreDebugLogState.file = nil
		coreDebugLogState.logger = nil
		return nil
	}
	if coreDebugLogState.enabled.Load() && coreDebugLogState.file != nil {
		return nil
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open backend debug log: %w", err)
	}
	coreDebugLogState.file = file
	coreDebugLogState.logger = log.New(file, "", log.LstdFlags)
	coreDebugLogState.enabled.Store(true)
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
	stopCoreOnExit := true
	if s.cachedProfiles != nil {
		stopCoreOnExit = s.cachedProfiles.Behavior.shouldStopCoreOnExit()
	} else if profiles, err := s.loadProfilesLocked(); err == nil {
		stopCoreOnExit = profiles.Behavior.shouldStopCoreOnExit()
	}
	keepCore := !stopCoreOnExit || s.keepCoreOnShutdown
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
		coreDebugf("service shutdown: keeping managed core running (stopCoreOnExit=%t, keepCoreOnShutdown=%t)", stopCoreOnExit, s.keepCoreOnShutdown)
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
		debugLogf("core", "save URL failed: %v", err)
		return CoreConfig{}, err
	}
	existing, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("core", "save URL failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}
	config, err := parseCoreURL(rawURL)
	if err != nil {
		debugLogf("core", "save URL failed to parse %q: %v", rawURL, err)
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
	config.StopCoreOnExit = existing.StopCoreOnExit
	config.TrayAPIURL = existing.TrayAPIURL
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		debugLogf("core", "save URL failed: config generation mismatch (current=%d, expected=%d)", s.configGeneration, generation)
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		debugLogf("core", "save URL failed to write config: %v", err)
		return CoreConfig{}, err
	}
	if owner, repository, repoErr := githubRepository(config.URLTemplate); repoErr == nil {
		s.clearCachedLatestRelease(owner, repository, config.Channel)
	}
	s.applyRuntimeState(&config)
	debugLogf("core", "save URL success: type=%s urlTemplate=%q", config.CoreType, config.URLTemplate)
	return config, nil
}

func (s *CoreService) SaveChannel(rawChannel, rawCoreType string) (CoreConfig, error) {
	channel, err := normalizeCoreChannel(rawChannel)
	if err != nil {
		debugLogf("core", "save channel failed: %v", err)
		return CoreConfig{}, err
	}
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		debugLogf("core", "save channel failed: %v", err)
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("core", "save channel failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}
	config.Channel = channel
	config.CorePath = s.corePathFor(config.CoreType, config.Channel)
	config.Installed = fileExistsCached(config.CorePath)
	config.Version = ""
	config.VersionDetail = ""
	config.InstalledVersion = ""
	s.applyCurrentVersion(&config, "")
	config.LatestVersion = ""
	config.UpdateAvailable = false
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		debugLogf("core", "save channel failed: config generation mismatch")
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		debugLogf("core", "save channel failed to write config: %v", err)
		return CoreConfig{}, err
	}
	if owner, repository, repoErr := githubRepository(config.URLTemplate); repoErr == nil {
		s.clearCachedLatestRelease(owner, repository, channel)
	}
	s.applyRuntimeState(&config)
	debugLogf("core", "save channel success: type=%s channel=%s installed=%t version=%s", config.CoreType, config.Channel, config.Installed, config.Version)
	return config, nil
}

func (s *CoreService) SaveRunArgs(rawArgs, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		debugLogf("core", "save run args failed: %v", err)
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("core", "save run args failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}
	config.RunArgs = strings.TrimSpace(rawArgs)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		debugLogf("core", "save run args failed: config generation mismatch")
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		debugLogf("core", "save run args failed to write config: %v", err)
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	debugLogf("core", "save run args success: type=%s runArgs=%q", config.CoreType, config.RunArgs)
	return config, nil
}

func (s *CoreService) SaveCoreType(rawCoreType string) (CoreConfig, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		debugLogf("core", "save core type failed: %v", err)
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("core", "save core type failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}
	if strings.TrimSpace(config.RunArgs) == "" || isDefaultCoreRunArgs(config.RunArgs) {
		config.RunArgs = defaultRunArgs(coreType)
	}
	config.CoreType = coreType
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != generation {
		debugLogf("core", "save core type failed: config generation mismatch")
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveConfigLocked(config); err != nil {
		debugLogf("core", "save core type failed to write config: %v", err)
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	debugLogf("core", "save core type success: activeCore=%s", config.CoreType)
	return config, nil
}

func (s *CoreService) SaveBehavior(runAsAdmin, autoStart, autoStartSingBox, autoStartMihomo, stopCoreOnExit, backendDebugLog bool, rawTrayAPIURL, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		debugLogf("system", "save behavior failed to normalize core type: %v", err)
		return CoreConfig{}, err
	}
	trayAPIURL, err := normalizeTrayAPIURL(rawTrayAPIURL)
	if err != nil {
		debugLogf("system", "save behavior invalid tray API URL %q: %v", rawTrayAPIURL, err)
		return CoreConfig{}, err
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("system", "save behavior failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}

	if err := writeRunAsAdminSetting(s.applicationPath, runAsAdmin); err != nil {
		debugLogf("system", "save behavior write RunAsAdmin failed: %v", err)
		return CoreConfig{}, err
	}
	if err := writeAutoStartSetting(s.applicationPath, autoStart); err != nil {
		debugLogf("system", "save behavior write auto start failed: %v", err)
		return CoreConfig{}, err
	}

	behavior := sharedBehaviorConfig{
		RunAsAdmin:       runAsAdmin,
		AutoStart:        autoStart,
		AutoStartSingBox: autoStartSingBox,
		AutoStartMihomo:  autoStartMihomo,
		BackendDebugLog:  backendDebugLog,
		StopCoreOnExit:   &stopCoreOnExit,
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
		debugLogf("system", "save behavior failed: config generation mismatch")
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}
	if err := s.saveBehaviorLocked(config, behavior); err != nil {
		debugLogf("system", "save behavior failed to write profiles: %v", err)
		return CoreConfig{}, err
	}
	s.trayAPIURL = trayAPIURL
	if err := configureCoreDebugLog(s.backendDebugLogPath(), backendDebugLog); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	debugLogf("system", "save behavior success: runAsAdmin=%t autoStart=%t debugLog=%t trayURL=%s", runAsAdmin, autoStart, backendDebugLog, trayAPIURL)
	return config, nil
}

func (s *CoreService) StartCore(rawArgs, rawCoreType string) (CoreConfig, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.startCore(rawArgs, rawCoreType, true)
}

func (s *CoreService) startCore(rawArgs, rawCoreType string, isPanelStart bool) (CoreConfig, error) {
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
			return CoreConfig{}, fmt.Errorf("check core status: %w", aliveErr)
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
		runningCoreType := s.processCoreType
		if runningCoreType == "" {
			runningCoreType = coreTypeSingBox
		}
		return CoreConfig{}, fmt.Errorf("%s core is already running", runningCoreType)
	}

	config, err := s.loadConfigForTypeLocked(coreType)
	if err != nil {
		return CoreConfig{}, err
	}
	if !fileExists(s.corePathFor(config.CoreType, config.Channel)) {
		return CoreConfig{}, fmt.Errorf("%s core is not installed", config.CoreType)
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
		return CoreConfig{}, fmt.Errorf("请输入 %s 命令行参数", config.CoreType)
	}
	coreDebugf("start request accepted: type=%s path=%q args=%d config=%t panelStart=%t", config.CoreType, s.corePathFor(config.CoreType, config.Channel), len(args), fileExists(s.configFilePath(config)), isPanelStart)

	if err := os.MkdirAll(s.coreDirFor(config.CoreType), 0o755); err != nil {
		return CoreConfig{}, fmt.Errorf("create core directory: %w", err)
	}
	s.cleanCoreLogs(config.CoreType)
	logFile, err := os.OpenFile(s.logFilePath(config.CoreType), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return CoreConfig{}, fmt.Errorf("open core log: %w", err)
	}

	command := exec.Command(s.corePathFor(config.CoreType, config.Channel), args...)
	command.Dir = s.coreDirFor(config.CoreType)
	command.Stdout = logFile
	command.Stderr = logFile
	configureCoreCommand(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		coreDebugf("start core process failed: type=%s err=%v", config.CoreType, err)
		return CoreConfig{}, fmt.Errorf("start %s core: %w", config.CoreType, err)
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
	s.coreLogError[config.CoreType] = false
	go s.waitForCore(command, logFile, done, config.CoreType, isPanelStart)

	go func(appPath string) {
		if err := ensureProgramDataShortcut(appPath); err != nil {
			coreDebugf("ensure start menu shortcut failed: %v", err)
		}
	}(s.applicationPath)

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
	s.coreLogError[config.CoreType] = false
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
	return s.startCore(rawArgs, coreType, true)
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
		return errors.New("core process state is invalid")
	}

	s.mu.Lock()
	if process != nil && process.Process != nil {
		s.stoppingPids[process.Process.Pid] = true
	}
	s.mu.Unlock()

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
		return fmt.Errorf("stop core: %w", err)
	}
	select {
	case <-done:
		coreDebugf("core exited after force kill: pid=%d", process.Process.Pid)
		return nil
	case <-time.After(5 * time.Second):
		coreDebugf("core stop timed out: pid=%d", process.Process.Pid)
		return errors.New("timed out waiting for core to stop")
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
		return fmt.Errorf("stop external core: %w", err)
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
			return errors.New("timed out waiting for external core to stop")
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

func (s *CoreService) waitForCore(command *exec.Cmd, logFile *os.File, done chan struct{}, coreType string, isPanelStart bool) {
	err := command.Wait()
	if err == nil {
		coreDebugf("process exited: pid=%d path=%q", command.Process.Pid, command.Path)
	} else {
		coreDebugf("process exited with error: pid=%d path=%q err=%v", command.Process.Pid, command.Path, err)
	}
	if err != nil {
		_, _ = fmt.Fprintf(logFile, "\n[%s exited: %v]\n", coreType, err)
	}
	_ = logFile.Close()

	s.mu.Lock()
	pid := 0
	if command.Process != nil {
		pid = command.Process.Pid
	}
	wasStopping := s.stoppingPids[pid]
	delete(s.stoppingPids, pid)

	if s.process == command {
		s.process = nil
		s.processDone = nil
		s.processCoreType = ""
	}

	if !wasStopping && isPanelStart {
		s.coreLogError[coreType] = true
		coreDebugf("core %s exited during/after panel start: pid=%d err=%v", coreType, pid, err)
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
	config.ConfigAvailable = fileExistsCached(config.ConfigPath)
	config.IsAdmin = isPrivilegedCached()
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
	config.UpdateAvailable = isCoreUpdateAvailable(config.LatestVersion, config.Version, config.Channel)
	config.CoreLogError = s.coreLogError[config.CoreType] && !config.Running
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
	channel := coreChannelStable
	if s.cachedProfiles != nil {
		if p, ok := s.cachedProfiles.Profiles[coreType]; ok && p.Channel != "" {
			channel = p.Channel
		}
	}
	process, err := findExternalCoreProcess(coreType, s.corePathFor(coreType, channel))
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
	shouldStart := err == nil && fileExists(s.corePathFor(config.CoreType, config.Channel)) && fileExists(s.configFilePath(config))
	runArgs := config.RunArgs
	coreDebugf("startup check: loadErr=%v autoStartCoreType=%s coreInstalled=%t configAvailable=%t", err, coreType, fileExists(s.corePathFor(config.CoreType, config.Channel)), fileExists(s.configFilePath(config)))
	if !shouldStart || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	s.trayAPIURL = config.TrayAPIURL
	s.mu.Unlock()
	if _, err := s.startCore(runArgs, config.CoreType, false); err != nil {
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
	if config.Channel == "" {
		config.Channel = coreChannelStable
	}
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
	config.CorePath = s.corePathFor(config.CoreType, config.Channel)
	config.Installed = fileExistsCached(config.CorePath)
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
		stopCoreOnExit := true
		profiles := persistedCoreProfiles{
			ActiveCore: coreTypeSingBox,
			Behavior: sharedBehaviorConfig{
				StopCoreOnExit: &stopCoreOnExit,
			},
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
		debugLogf("core", "parse profiles.json failed: %v", err)
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
	config.StopCoreOnExit = behavior.shouldStopCoreOnExit()
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
		debugLogf("core", "save config failed to load profiles: %v", err)
		return err
	}
	config.CoreType = normalizedCoreType(config.CoreType)
	if config.Channel == "" {
		config.Channel = coreChannelStable
	}
	config.CorePath = s.corePathFor(config.CoreType, config.Channel)
	if activate {
		profiles.ActiveCore = config.CoreType
	}
	profiles.Profiles[config.CoreType] = config
	return s.writeProfilesLocked(profiles)
}

func (s *CoreService) saveBehaviorLocked(config CoreConfig, behavior sharedBehaviorConfig) error {
	profiles, err := s.loadProfilesLocked()
	if err != nil {
		debugLogf("core", "save behavior failed to load profiles: %v", err)
		return err
	}
	config.CoreType = normalizedCoreType(config.CoreType)
	if config.Channel == "" {
		config.Channel = coreChannelStable
	}
	config.CorePath = s.corePathFor(config.CoreType, config.Channel)
	profiles.Behavior = behavior
	profiles.Profiles[config.CoreType] = config
	return s.writeProfilesLocked(profiles)
}

func (s *CoreService) writeProfilesLocked(profiles persistedCoreProfiles) error {
	data, err := marshalPersistedCoreProfiles(profiles)
	if err != nil {
		debugLogf("core", "marshal profiles failed: %v", err)
		return err
	}
	data = append(data, '\n')
	configPath := s.configPath()
	if err := writeFileAtomically(configPath, data, 0o600); err != nil {
		debugLogf("core", "write profiles atomically to %q failed: %v", configPath, err)
		return err
	}
	if stat, err := os.Stat(configPath); err == nil {
		s.configModTime = stat.ModTime()
	}
	s.cachedProfiles = &profiles
	s.configGeneration++
	return nil
}

type persistedProfileClean struct {
	CoreType       string `json:"coreType,omitempty"`
	URLTemplate    string `json:"urlTemplate,omitempty"`
	Version        string `json:"version,omitempty"`
	VersionDetail  string `json:"versionDetail,omitempty"`
	Channel        string `json:"channel,omitempty"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	RunArgs        string `json:"runArgs,omitempty"`
	ConfigURL      string `json:"configURL,omitempty"`
	ConfigFileName string `json:"configFileName,omitempty"`
	TrayAPIURL     string `json:"trayAPIURL,omitempty"`
}

func marshalPersistedCoreProfiles(profiles persistedCoreProfiles) ([]byte, error) {
	clean := struct {
		ActiveCore string                             `json:"activeCore"`
		Behavior   sharedBehaviorConfig               `json:"behavior"`
		Profiles   map[string]persistedProfileClean   `json:"profiles"`
	}{
		ActiveCore: profiles.ActiveCore,
		Behavior:   profiles.Behavior,
		Profiles:   make(map[string]persistedProfileClean, len(profiles.Profiles)),
	}
	for key, p := range profiles.Profiles {
		clean.Profiles[key] = persistedProfileClean{
			CoreType:       p.CoreType,
			URLTemplate:    p.URLTemplate,
			Version:        p.Version,
			VersionDetail:  p.VersionDetail,
			Channel:        p.Channel,
			LatestVersion:  p.LatestVersion,
			RunArgs:        p.RunArgs,
			ConfigURL:      p.ConfigURL,
			ConfigFileName: p.ConfigFileName,
			TrayAPIURL:     p.TrayAPIURL,
		}
	}
	return json.MarshalIndent(clean, "", "  ")
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

func defaultRunArgs(coreType string) string {
	if normalizedCoreType(coreType) == coreTypeMihomo {
		return defaultMihomoRunArgs
	}
	return defaultCoreRunArgs
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

func (s *CoreService) corePathFor(coreType, channel string) string {
	return filepath.Join(s.coreDirFor(coreType), coreExecutableNameFor(coreType, channel))
}

func (s *CoreService) logFilePath(coreType string) string {
	return filepath.Join(s.coreDirFor(coreType), "core.log")
}

func (s *CoreService) OpenCoreLog(rawCoreType string) error {
	s.mu.Lock()
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		if config, loadErr := s.loadConfigLocked(); loadErr == nil {
			coreType = config.CoreType
		} else {
			coreType = coreTypeSingBox
		}
	}
	logPath := s.logFilePath(coreType)
	s.mu.Unlock()

	if !fileExists(logPath) {
		return fmt.Errorf("日志文件不存在: %s", logPath)
	}

	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(logPath)
	if err != nil {
		debugLogf("core", "convert log path to utf16 failed: %v", err)
		return err
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err == nil {
		debugLogf("core", "opened core log: %s", logPath)
		return nil
	}
	cmd := exec.Command("cmd", "/c", "start", "", logPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		debugLogf("core", "open core log failed: %v", err)
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	debugLogf("core", "opened core log via cmd fallback: %s", logPath)
	return nil
}

func (s *CoreService) configFilePath(config CoreConfig) string {
	fileName, err := normalizeConfigFileName(config.ConfigFileName, config.CoreType)
	if err != nil {
		fileName = defaultConfigFileName(config.CoreType)
	}
	return filepath.Join(s.coreDirFor(config.CoreType), fileName)
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

const fileExistsCacheTTL = 5 * time.Second

type fileExistsCacheEntry struct {
	exists    bool
	checkedAt time.Time
}

var fileExistsCacheState struct {
	sync.Mutex
	entries map[string]fileExistsCacheEntry
}

func fileExistsCached(path string) bool {
	fileExistsCacheState.Lock()
	if fileExistsCacheState.entries != nil {
		if entry, ok := fileExistsCacheState.entries[path]; ok && time.Since(entry.checkedAt) < fileExistsCacheTTL {
			fileExistsCacheState.Unlock()
			return entry.exists
		}
	}
	fileExistsCacheState.Unlock()

	exists := fileExists(path)

	fileExistsCacheState.Lock()
	if fileExistsCacheState.entries == nil {
		fileExistsCacheState.entries = make(map[string]fileExistsCacheEntry)
	}
	fileExistsCacheState.entries[path] = fileExistsCacheEntry{exists: exists, checkedAt: time.Now()}
	fileExistsCacheState.Unlock()
	return exists
}

var privilegedCacheState struct {
	sync.Once
	isAdmin bool
}

func isPrivilegedCached() bool {
	privilegedCacheState.Once.Do(func() {
		if admin, err := isPrivileged(); err == nil {
			privilegedCacheState.isAdmin = admin
		}
	})
	return privilegedCacheState.isAdmin
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





