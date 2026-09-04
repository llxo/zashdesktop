package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

func (s *CoreService) commitConfigUpdate(config CoreConfig, generation uint64) (CoreConfig, error) {
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

func (s *CoreService) saveCheckedConfig(config CoreConfig, generation uint64) (CoreConfig, error) {
	return s.commitConfigUpdate(config, generation)
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

	activeConfigFile := config.ActiveConfigFile
	if strings.TrimSpace(activeConfigFile) == "" && strings.TrimSpace(config.ConfigFileName) != "" {
		activeConfigFile = config.ConfigFileName
	}
	normalizedActive, activeErr := normalizeConfigFileName(activeConfigFile, coreType)
	if activeErr != nil {
		normalizedActive = defaultConfigFileName(coreType)
	}
	config.ActiveConfigFile = normalizedActive
	if strings.TrimSpace(config.TrayAPIURL) == "" {
		config.TrayAPIURL = defaultTrayAPIURL
	}
	applySharedBehavior(&config, profiles.Behavior)
	s.applySystemBehavior(&config)
	config.CorePath = s.corePathFor(config.CoreType, config.Channel)
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
		stopCoreOnExit := true
		profiles := persistedCoreProfiles{
			ActiveCore: coreTypeSingBox,
			Behavior: sharedBehaviorConfig{
				StopCoreOnExit: &stopCoreOnExit,
			},
			Profiles: make(map[string]CoreConfig),
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
	CoreType         string `json:"coreType,omitempty"`
	URLTemplate      string `json:"urlTemplate,omitempty"`
	Version          string `json:"version,omitempty"`
	VersionDetail    string `json:"versionDetail,omitempty"`
	Channel          string `json:"channel,omitempty"`
	LatestVersion    string `json:"latestVersion,omitempty"`
	RunArgs          string `json:"runArgs,omitempty"`
	ConfigURL        string `json:"configURL,omitempty"`
	ConfigFileName   string `json:"configFileName,omitempty"`
	ActiveConfigFile string `json:"activeConfigFile,omitempty"`
	TrayAPIURL       string `json:"trayAPIURL,omitempty"`
}

func marshalPersistedCoreProfiles(profiles persistedCoreProfiles) ([]byte, error) {
	clean := struct {
		ActiveCore string                           `json:"activeCore"`
		Behavior   sharedBehaviorConfig             `json:"behavior"`
		Profiles   map[string]persistedProfileClean `json:"profiles"`
	}{
		ActiveCore: profiles.ActiveCore,
		Behavior:   profiles.Behavior,
		Profiles:   make(map[string]persistedProfileClean, len(profiles.Profiles)),
	}
	for key, p := range profiles.Profiles {
		clean.Profiles[key] = persistedProfileClean{
			CoreType:         p.CoreType,
			URLTemplate:      p.URLTemplate,
			Version:          p.Version,
			VersionDetail:    p.VersionDetail,
			Channel:          p.Channel,
			LatestVersion:    p.LatestVersion,
			RunArgs:          p.RunArgs,
			ConfigURL:        p.ConfigURL,
			ConfigFileName:   p.ConfigFileName,
			ActiveConfigFile: p.ActiveConfigFile,
			TrayAPIURL:       p.TrayAPIURL,
		}
	}
	return json.MarshalIndent(clean, "", "  ")
}

func (s *CoreService) configPath() string {
	return filepath.Join(s.executableDir, "profiles.json")
}

func (s *CoreService) configFilePath(config CoreConfig) string {
	fileName, err := normalizeConfigFileName(config.ActiveConfigFile, config.CoreType)
	if err != nil {
		fileName = defaultConfigFileName(config.CoreType)
	}
	return filepath.Join(s.coreDirFor(config.CoreType), fileName)
}

func (s *CoreService) saveConfigFilePath(config CoreConfig) string {
	fileName, err := normalizeConfigFileName(config.ConfigFileName, config.CoreType)
	if err != nil {
		fileName = defaultConfigFileName(config.CoreType)
	}
	return filepath.Join(s.coreDirFor(config.CoreType), fileName)
}
