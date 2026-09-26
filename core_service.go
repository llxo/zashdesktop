package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
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
	ClashAPIURL       string `json:"clashApiUrl"`
	ClashAPIHost      string `json:"clashApiHost"`
	ClashAPIPort      string `json:"clashApiPort"`
	ClashAPISecret    string `json:"clashApiSecret"`
}

type CoreSettingsPatch struct {
	CoreType         string  `json:"coreType"`
	Channel          *string `json:"channel,omitempty"`
	RunArgs          *string `json:"runArgs,omitempty"`
	RunAsAdmin       *bool   `json:"runAsAdmin,omitempty"`
	AutoStart        *bool   `json:"autoStart,omitempty"`
	AutoStartSingBox *bool   `json:"autoStartSingBox,omitempty"`
	AutoStartMihomo  *bool   `json:"autoStartMihomo,omitempty"`
	StopCoreOnExit   *bool   `json:"stopCoreOnExit,omitempty"`
	BackendDebugLog  *bool   `json:"backendDebugLog,omitempty"`
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
	// inheritedProcess: 上次应用退出时保留运行（stopCoreOnExit 为 false）的核心进程，新应用启动后接管
	inheritedProcess   *os.Process
	inheritedCoreType  string
	stateLogged        bool
	lastRunning        bool
	lastPID            int
	runningClashAPIURL    string
	runningClashAPIHost   string
	runningClashAPIPort   string
	runningClashAPISecret string
	keepCoreOnShutdown    bool
	onStateChange         func()
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

func (s *CoreService) emitStateChangeEvent(app *application.App) {
	if app != nil && app.Event != nil {
		go app.Event.Emit("core:state-changed")
	}
}

func (s *CoreService) notifyStateChange() {
	s.mu.Lock()
	cb := s.onStateChange
	app := s.app
	s.mu.Unlock()
	if cb != nil {
		go cb()
	}
	s.emitStateChangeEvent(app)
}

func (s *CoreService) notifyStateChangeLocked() {
	cb := s.onStateChange
	app := s.app
	if cb != nil {
		go cb()
	}
	s.emitStateChangeEvent(app)
}

func (s *CoreService) GetConfig() (CoreConfig, error) {
	s.mu.Lock()
	config, err := s.loadConfigLocked()
	if err != nil {
		s.mu.Unlock()
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	s.mu.Unlock()

	s.applyCurrentVersion(&config, "")
	return config, nil
}

func (s *CoreService) GetConfigForType(rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	s.mu.Lock()
	config, err := s.loadConfigForTypeLocked(coreType)
	if err != nil {
		s.mu.Unlock()
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	s.mu.Unlock()

	s.applyCurrentVersion(&config, "")
	return config, nil
}

func (s *CoreService) SaveCoreType(rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		debugLogf("core", "save core type failed: %v", err)
		return CoreConfig{}, err
	}
	config, _, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("core", "save core type failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}
	if strings.TrimSpace(config.RunArgs) == "" || isDefaultCoreRunArgs(config.RunArgs) {
		config.RunArgs = defaultRunArgs(coreType)
	}
	config.CoreType = coreType
	saved, err := s.commitConfigUpdate(config)
	if err != nil {
		debugLogf("core", "save core type failed: %v", err)
		return CoreConfig{}, err
	}
	debugLogf("core", "save core type success: activeCore=%s", saved.CoreType)
	return saved, nil
}

func (s *CoreService) UpdateCoreSettings(patch CoreSettingsPatch) (CoreConfig, error) {
	coreType, err := normalizeCoreType(patch.CoreType)
	if err != nil {
		debugLogf("core", "update settings failed to normalize core type: %v", err)
		return CoreConfig{}, err
	}

	config, _, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		debugLogf("core", "update settings failed to load snapshot: %v", err)
		return CoreConfig{}, err
	}

	if patch.Channel != nil {
		channel, err := normalizeCoreChannel(*patch.Channel)
		if err != nil {
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
	}

	if patch.RunArgs != nil {
		config.RunArgs = strings.TrimSpace(*patch.RunArgs)
	}

	hasBehavior := patch.RunAsAdmin != nil || patch.AutoStart != nil ||
		patch.AutoStartSingBox != nil || patch.AutoStartMihomo != nil ||
		patch.StopCoreOnExit != nil || patch.BackendDebugLog != nil

	if hasBehavior {
		runAsAdmin := config.RunAsAdmin
		if patch.RunAsAdmin != nil {
			runAsAdmin = *patch.RunAsAdmin
		}
		autoStart := config.AutoStart
		if patch.AutoStart != nil {
			autoStart = *patch.AutoStart
		}
		autoStartSingBox := config.AutoStartSingBox
		if patch.AutoStartSingBox != nil {
			autoStartSingBox = *patch.AutoStartSingBox
		}
		autoStartMihomo := config.AutoStartMihomo
		if patch.AutoStartMihomo != nil {
			autoStartMihomo = *patch.AutoStartMihomo
		}
		backendDebugLog := config.BackendDebugLog
		if patch.BackendDebugLog != nil {
			backendDebugLog = *patch.BackendDebugLog
		}
		stopCoreOnExit := config.StopCoreOnExit
		if patch.StopCoreOnExit != nil {
			stopCoreOnExit = *patch.StopCoreOnExit
		}

		if err := writeRunAsAdminSetting(s.applicationPath, runAsAdmin); err != nil {
			debugLogf("system", "update settings write RunAsAdmin failed: %v", err)
			return CoreConfig{}, err
		}
		if err := writeAutoStartSetting(s.applicationPath, autoStart); err != nil {
			debugLogf("system", "update settings write auto start failed: %v", err)
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

		s.mu.Lock()
		if currentConfig, err := s.loadConfigForTypeLocked(coreType); err == nil {
			config = currentConfig
			applySharedBehavior(&config, behavior)
		}
		if err := s.saveBehaviorLocked(config, behavior); err != nil {
			s.mu.Unlock()
			debugLogf("system", "update settings failed to save behavior: %v", err)
			return CoreConfig{}, err
		}
		s.mu.Unlock()

		if err := configureCoreDebugLog(s.backendDebugLogPath(), backendDebugLog); err != nil {
			return CoreConfig{}, err
		}
	}

	saved, err := s.commitConfigUpdate(config)
	if err != nil {
		debugLogf("core", "update settings commit failed: %v", err)
		return CoreConfig{}, err
	}

	debugLogf("core", "update settings success: core=%s channel=%s", saved.CoreType, saved.Channel)
	return saved, nil
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
	s.detectAnyInheritedProcessLocked()
	if s.inheritedProcess != nil {
		return CoreConfig{}, fmt.Errorf("%s core is already running (PID %d)", s.inheritedCoreType, s.inheritedProcess.Pid)
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
	s.extractClashAPIFromConfig(&config)
	s.runningClashAPIURL = config.ClashAPIURL
	s.runningClashAPIHost = config.ClashAPIHost
	s.runningClashAPIPort = config.ClashAPIPort
	s.runningClashAPISecret = config.ClashAPISecret
	go s.waitForCore(command, logFile, done, config.CoreType, isPanelStart)

	go func(appPath string) {
		if err := ensureProgramDataShortcut(appPath); err != nil {
			coreDebugf("ensure start menu shortcut failed: %v", err)
		}
	}(s.applicationPath)

	if config.ClashAPIPort != "" {
		ready := waitForPortReady(config.ClashAPIHost, config.ClashAPIPort, 300*time.Millisecond, done)
		debugLogf("core", "clash API port readiness check: host=%s port=%s ready=%t", config.ClashAPIHost, config.ClashAPIPort, ready)
		if !ready {
			go s.pollPortReadyAsync(config.ClashAPIHost, config.ClashAPIPort, 5*time.Second, done)
		}
	}

	s.applyRuntimeState(&config)
	s.notifyStateChangeLocked()
	return config, nil
}

func (s *CoreService) pollPortReadyAsync(host, port string, timeout time.Duration, done chan struct{}) {
	ready := waitForPortReady(host, port, timeout, done)
	if ready {
		debugLogf("core", "clash API port became ready asynchronously: host=%s port=%s", host, port)
		s.notifyStateChange()
	} else {
		debugLogf("core", "clash API port failed to become ready within %v: host=%s port=%s", timeout, host, port)
	}
}

func waitForPortReady(host, port string, timeout time.Duration, done chan struct{}) bool {
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	target := net.JoinHostPort(host, port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return false
		default:
		}
		conn, err := net.DialTimeout("tcp", target, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		select {
		case <-done:
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
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
	invalidateProxySettingsCache()

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
	s.detectAnyInheritedProcessLocked()
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
	if s.inheritedProcess != nil && s.inheritedCoreType != coreType {
		runningCoreType := s.inheritedCoreType
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
	inherited := s.inheritedProcess
	s.mu.Unlock()
	if process == nil && inherited == nil {
		coreDebugf("stop request: no core process")
		return nil
	}
	if process != nil {
		return s.stopManagedProcess(process, done)
	}
	return s.stopInheritedProcess(inherited)
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

func (s *CoreService) stopInheritedProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	alive, statusErr := coreProcessAlive(process)
	coreDebugf("stop inherited request: pid=%d alive=%t checkErr=%v", process.Pid, alive, statusErr)
	if statusErr == nil && !alive {
		s.clearInheritedProcess(process)
		return nil
	}
	if err := requestCoreStop(process); err != nil {
		coreDebugf("inherited graceful stop signal failed: pid=%d err=%v", process.Pid, err)
	} else {
		coreDebugf("inherited graceful stop signal sent: pid=%d", process.Pid)
	}
	if waitErr := waitForCoreProcessExit(process, 5*time.Second); waitErr == nil {
		coreDebugf("inherited core exited after graceful stop: pid=%d", process.Pid)
		s.clearInheritedProcess(process)
		return nil
	}

	coreDebugf("inherited graceful stop timed out, forcing kill: pid=%d", process.Pid)
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop inherited core: %w", err)
	}
	if err := waitForCoreProcessExit(process, 5*time.Second); err != nil {
		coreDebugf("inherited core stop timed out: pid=%d", process.Pid)
		return err
	}
	s.clearInheritedProcess(process)
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
			return errors.New("timed out waiting for inherited core to stop")
		case <-ticker.C:
		}
	}
}

func (s *CoreService) clearInheritedProcess(process *os.Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inheritedProcess == process {
		s.inheritedProcess = nil
		s.inheritedCoreType = ""
		if s.process == nil {
			s.runningClashAPIURL = ""
			s.runningClashAPIHost = ""
			s.runningClashAPIPort = ""
			s.runningClashAPISecret = ""
		}
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
		s.runningClashAPIURL = ""
		s.runningClashAPIHost = ""
		s.runningClashAPIPort = ""
		s.runningClashAPISecret = ""
	}

	if !wasStopping && isPanelStart {
		s.coreLogError[coreType] = true
		coreDebugf("core %s exited during/after panel start: pid=%d err=%v", coreType, pid, err)
	}
	s.mu.Unlock()
	close(done)
	invalidateProxySettingsCache()
	s.notifyStateChange()
}

func (s *CoreService) applyRuntimeState(config *CoreConfig) {
	config.CoreType = normalizedCoreType(config.CoreType)
	s.detectInheritedProcessLocked(config.CoreType)
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
	if !config.Running && s.inheritedProcess != nil && s.inheritedCoreType == config.CoreType {
		alive, err := coreProcessAlive(s.inheritedProcess)
		if err != nil {
			coreDebugf("inherited runtime status check failed: pid=%d err=%v", s.inheritedProcess.Pid, err)
		} else if alive {
			config.Running = true
			config.PID = s.inheritedProcess.Pid
		} else {
			s.inheritedProcess = nil
			s.inheritedCoreType = ""
		}
	}
	if config.Running && s.process != nil && s.processCoreType == config.CoreType {
		config.PID = s.process.Process.Pid
	}
	config.UpdateAvailable = isCoreUpdateAvailable(config.LatestVersion, config.Version, config.Channel)
	config.CoreLogError = s.coreLogError[config.CoreType] && !config.Running
	stateChanged := s.stateLogged && (s.lastRunning != config.Running || s.lastPID != config.PID)
	if !s.stateLogged || s.lastRunning != config.Running || s.lastPID != config.PID {
		coreDebugf("runtime state changed: running=%t pid=%d type=%s", config.Running, config.PID, config.CoreType)
		s.stateLogged = true
		s.lastRunning = config.Running
		s.lastPID = config.PID
	}
	if stateChanged {
		s.notifyStateChangeLocked()
	}
	if config.Running {
		config.ClashAPIURL = s.runningClashAPIURL
		config.ClashAPIHost = s.runningClashAPIHost
		config.ClashAPIPort = s.runningClashAPIPort
		config.ClashAPISecret = s.runningClashAPISecret
	} else {
		config.ClashAPIURL = ""
		config.ClashAPIHost = ""
		config.ClashAPIPort = ""
		config.ClashAPISecret = ""
	}
}

func (s *CoreService) detectInheritedProcessLocked(coreType string) {
	if s.process != nil {
		return
	}
	if s.inheritedProcess != nil {
		alive, err := coreProcessAlive(s.inheritedProcess)
		if err == nil && alive {
			return
		}
		coreDebugf("inherited core process no longer available: pid=%d err=%v", s.inheritedProcess.Pid, err)
		s.inheritedProcess = nil
		s.inheritedCoreType = ""
		if s.process == nil {
			s.runningClashAPIURL = ""
			s.runningClashAPIHost = ""
			s.runningClashAPIPort = ""
			s.runningClashAPISecret = ""
		}
	}
	channel := coreChannelStable
	if s.cachedProfiles != nil {
		if p, ok := s.cachedProfiles.Profiles[coreType]; ok && p.Channel != "" {
			channel = p.Channel
		}
	}
	process, err := findInheritedCoreProcess(coreType, s.corePathFor(coreType, channel))
	if err != nil {
		coreDebugf("inherited core detection failed: type=%s err=%v", coreType, err)
		return
	}
	if process != nil {
		s.inheritedProcess = process
		s.inheritedCoreType = coreType
		coreDebugf("inherited core process detected: type=%s pid=%d", coreType, process.Pid)
		if s.runningClashAPIURL == "" {
			if cfg, err := s.loadConfigForTypeLocked(coreType); err == nil {
				s.extractClashAPIFromConfig(&cfg)
				s.runningClashAPIURL = cfg.ClashAPIURL
				s.runningClashAPIHost = cfg.ClashAPIHost
				s.runningClashAPIPort = cfg.ClashAPIPort
				s.runningClashAPISecret = cfg.ClashAPISecret
			}
		}
	}
}

func (s *CoreService) detectAnyInheritedProcessLocked() {
	if s.process != nil {
		return
	}
	s.detectInheritedProcessLocked(coreTypeSingBox)
	if s.inheritedProcess == nil {
		s.detectInheritedProcessLocked(coreTypeMihomo)
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
