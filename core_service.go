package main

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	defaultCoreRunArgs      = `run -c "config.json" -D .`
	defaultMihomoRunArgs    = `-d . -f "config.yaml"`
)

type CoreConfig struct {
	CoreType         string `json:"coreType"`
	URLTemplate      string `json:"urlTemplate"`
	Version          string `json:"version"`
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
	ActiveConfigFile  string `json:"activeConfigFile"`
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
	cleanURL := strings.TrimSpace(rawURL)
	if cleanURL != "" {
		if err := validateHTTPURL(cleanURL, "核心下载地址"); err != nil {
			debugLogf("core", "save URL failed invalid url: %v", err)
			return CoreConfig{}, err
		}
		if _, _, err := githubRepository(cleanURL); err != nil {
			debugLogf("core", "save URL failed invalid github repo: %v", err)
			return CoreConfig{}, err
		}
	}
	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("core", "save URL failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}
	config.URLTemplate = cleanURL
	config.LatestVersion = ""
	config.UpdateAvailable = false

	saved, err := s.commitConfigUpdate(config, generation)
	if err != nil {
		debugLogf("core", "save URL failed: %v", err)
		return CoreConfig{}, err
	}
	if owner, repository, repoErr := githubRepository(saved.URLTemplate); repoErr == nil {
		s.clearCachedLatestRelease(owner, repository, saved.Channel)
	}
	debugLogf("core", "save URL success: type=%s urlTemplate=%q", saved.CoreType, saved.URLTemplate)
	return saved, nil
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
	config.Installed = fileExists(config.CorePath)
	config.Version = ""
	config.VersionDetail = ""
	config.InstalledVersion = ""
	s.applyCurrentVersion(&config, "")
	config.LatestVersion = ""
	config.UpdateAvailable = false

	saved, err := s.commitConfigUpdate(config, generation)
	if err != nil {
		debugLogf("core", "save channel failed: %v", err)
		return CoreConfig{}, err
	}
	if owner, repository, repoErr := githubRepository(saved.URLTemplate); repoErr == nil {
		s.clearCachedLatestRelease(owner, repository, channel)
	}
	debugLogf("core", "save channel success: type=%s channel=%s installed=%t version=%s", saved.CoreType, saved.Channel, saved.Installed, saved.Version)
	return saved, nil
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
	saved, err := s.commitConfigUpdate(config, generation)
	if err != nil {
		debugLogf("core", "save run args failed: %v", err)
		return CoreConfig{}, err
	}
	debugLogf("core", "save run args success: type=%s runArgs=%q", saved.CoreType, saved.RunArgs)
	return saved, nil
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
	saved, err := s.commitConfigUpdate(config, generation)
	if err != nil {
		debugLogf("core", "save core type failed: %v", err)
		return CoreConfig{}, err
	}
	debugLogf("core", "save core type success: activeCore=%s", saved.CoreType)
	return saved, nil
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
	config.ConfigAvailable = fileExists(config.ConfigPath)
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
