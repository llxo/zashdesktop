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

const currentSchemaVersion = 1

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

type persistedProfileItem struct {
	CoreType       string `json:"coreType"`
	Version        string `json:"version,omitempty"`
	VersionDetail  string `json:"versionDetail,omitempty"`
	Channel        string `json:"channel,omitempty"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	RunArgs        string `json:"runArgs,omitempty"`
	ConfigURL      string `json:"configURL,omitempty"`
	ConfigFileName string `json:"configFileName,omitempty"`
}

type persistedCoreProfiles struct {
	SchemaVersion int                             `json:"schemaVersion"`
	ActiveCore    string                          `json:"activeCore"`
	Behavior      sharedBehaviorConfig            `json:"behavior"`
	Profiles      map[string]persistedProfileItem `json:"profiles"`
}

func defaultProfileItem(coreType string) persistedProfileItem {
	coreType = normalizedCoreType(coreType)
	return persistedProfileItem{
		CoreType:       coreType,
		Channel:        coreChannelStable,
		RunArgs:        defaultRunArgs(coreType),
		ConfigFileName: defaultConfigFileName(coreType),
	}
}

func defaultPersistedProfiles() persistedCoreProfiles {
	stopCoreOnExit := true
	return persistedCoreProfiles{
		SchemaVersion: currentSchemaVersion,
		ActiveCore:    coreTypeSingBox,
		Behavior: sharedBehaviorConfig{
			StopCoreOnExit: &stopCoreOnExit,
		},
		Profiles: map[string]persistedProfileItem{
			coreTypeSingBox: defaultProfileItem(coreTypeSingBox),
			coreTypeMihomo:  defaultProfileItem(coreTypeMihomo),
		},
	}
}

func (s *CoreService) profileItemToConfig(item persistedProfileItem, behavior sharedBehaviorConfig) CoreConfig {
	coreType := normalizedCoreType(item.CoreType)
	channel := strings.TrimSpace(item.Channel)
	if channel == "" {
		channel = coreChannelStable
	}
	configFileName, err := normalizeConfigFileName(item.ConfigFileName, coreType)
	if err != nil {
		configFileName = defaultConfigFileName(coreType)
	}
	runArgs := strings.TrimSpace(item.RunArgs)
	if runArgs == "" {
		runArgs = defaultRunArgs(coreType)
	}

	config := CoreConfig{
		CoreType:       coreType,
		Version:        item.Version,
		VersionDetail:  item.VersionDetail,
		Channel:        channel,
		LatestVersion:  item.LatestVersion,
		RunArgs:        runArgs,
		ConfigURL:      item.ConfigURL,
		ConfigFileName: configFileName,
	}
	applySharedBehavior(&config, behavior)
	config.CorePath = s.corePathFor(config.CoreType, config.Channel)
	config.Installed = fileExists(config.CorePath)
	if cached, ok := s.getCachedCoreVersion(config.CoreType, config.Channel); ok {
		config.Version = cached.version
		config.VersionDetail = cached.detail
		config.InstalledVersion = cached.version
	}
	return config
}

func configToProfileItem(config CoreConfig) persistedProfileItem {
	coreType := normalizedCoreType(config.CoreType)
	channel := strings.TrimSpace(config.Channel)
	if channel == "" {
		channel = coreChannelStable
	}
	configFileName, err := normalizeConfigFileName(config.ConfigFileName, coreType)
	if err != nil {
		configFileName = defaultConfigFileName(coreType)
	}
	runArgs := strings.TrimSpace(config.RunArgs)
	if runArgs == "" {
		runArgs = defaultRunArgs(coreType)
	}
	return persistedProfileItem{
		CoreType:       coreType,
		Version:        config.Version,
		VersionDetail:  config.VersionDetail,
		Channel:        channel,
		LatestVersion:  config.LatestVersion,
		RunArgs:        runArgs,
		ConfigURL:      config.ConfigURL,
		ConfigFileName: configFileName,
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
	generation := s.configGeneration
	config, err := s.loadConfigForTypeLocked(coreType)
	s.mu.Unlock()
	if err != nil {
		return CoreConfig{}, generation, err
	}
	s.applyCurrentVersion(&config, "")
	return config, generation, nil
}

func (s *CoreService) commitConfigUpdate(config CoreConfig) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) applyCheckedConfig(config CoreConfig) (CoreConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) loadProfileFromStoreLocked(profiles persistedCoreProfiles, coreType string) (CoreConfig, error) {
	normCore := normalizedCoreType(coreType)
	item, ok := profiles.Profiles[normCore]
	if !ok {
		item = defaultProfileItem(normCore)
	}
	return s.profileItemToConfig(item, profiles.Behavior), nil
}

func (s *CoreService) restoreProfilesFromBackupLocked(reason string) (persistedCoreProfiles, bool) {
	backupData, backupErr := os.ReadFile(s.configBackupPath())
	if backupErr == nil && len(backupData) > 0 {
		var backupProfiles persistedCoreProfiles
		if unmarshalErr := json.Unmarshal(backupData, &backupProfiles); unmarshalErr == nil {
			debugLogf("core", "profiles.json %s, successfully restored from backup", reason)
			s.normalizeLoadedProfiles(&backupProfiles)
			s.cachedProfiles = &backupProfiles
			_ = s.writeProfilesLocked(backupProfiles)
			return backupProfiles, true
		}
	}
	return persistedCoreProfiles{}, false
}

func (s *CoreService) loadProfilesLocked() (persistedCoreProfiles, error) {
	configPath := s.configPath()
	stat, statErr := os.Stat(configPath)
	if statErr == nil && s.cachedProfiles != nil && stat.ModTime().Equal(s.configModTime) {
		return *s.cachedProfiles, nil
	}

	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		// 主配置文件不存在时，尝试从备份恢复
		if backupProfiles, ok := s.restoreProfilesFromBackupLocked("missing"); ok {
			return backupProfiles, nil
		}

		// 备份亦不存在，创建标准默认配置
		profiles := defaultPersistedProfiles()
		s.cachedProfiles = &profiles
		s.configModTime = time.Time{}
		_ = s.writeProfilesLocked(profiles)
		return profiles, nil
	}
	if err != nil {
		if s.cachedProfiles != nil {
			return *s.cachedProfiles, nil
		}
		return defaultPersistedProfiles(), fmt.Errorf("read core config: %w", err)
	}

	var profiles persistedCoreProfiles
	if err := json.Unmarshal(data, &profiles); err != nil {
		debugLogf("core", "parse profiles.json failed: %v, attempting recovery from backup...", err)
		// 损坏时优先从 .bak 恢复
		if backupProfiles, ok := s.restoreProfilesFromBackupLocked("corrupted"); ok {
			return backupProfiles, nil
		}

		// 无可用备份，将损坏文件归档并用默认配置自愈
		corruptedArchive := s.configCorruptedPath()
		_ = os.Rename(configPath, corruptedArchive)
		debugLogf("core", "unrecoverable profiles.json archived to %q, self-healing with defaults", corruptedArchive)

		healingProfiles := defaultPersistedProfiles()
		s.cachedProfiles = &healingProfiles
		_ = s.writeProfilesLocked(healingProfiles)
		return healingProfiles, nil
	}

	s.normalizeLoadedProfiles(&profiles)
	s.cachedProfiles = &profiles
	if statErr == nil {
		s.configModTime = stat.ModTime()
	}
	return profiles, nil
}

func (s *CoreService) normalizeLoadedProfiles(profiles *persistedCoreProfiles) {
	if profiles.SchemaVersion <= 0 {
		profiles.SchemaVersion = currentSchemaVersion
	}
	if profiles.Profiles == nil {
		profiles.Profiles = make(map[string]persistedProfileItem)
	}
	if _, ok := profiles.Profiles[coreTypeSingBox]; !ok {
		profiles.Profiles[coreTypeSingBox] = defaultProfileItem(coreTypeSingBox)
	}
	if _, ok := profiles.Profiles[coreTypeMihomo]; !ok {
		profiles.Profiles[coreTypeMihomo] = defaultProfileItem(coreTypeMihomo)
	}
	if profiles.ActiveCore == "" {
		profiles.ActiveCore = coreTypeSingBox
	}
	profiles.ActiveCore = normalizedCoreType(profiles.ActiveCore)
	normalizeSharedBehavior(&profiles.Behavior, profiles.ActiveCore)
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
	normCore := normalizedCoreType(config.CoreType)
	config.CoreType = normCore
	if activate {
		profiles.ActiveCore = normCore
	}
	profiles.Profiles[normCore] = configToProfileItem(config)
	return s.writeProfilesLocked(profiles)
}

func (s *CoreService) saveBehaviorLocked(config CoreConfig, behavior sharedBehaviorConfig) error {
	profiles, err := s.loadProfilesLocked()
	if err != nil {
		debugLogf("core", "save behavior failed to load profiles: %v", err)
		return err
	}
	normCore := normalizedCoreType(config.CoreType)
	config.CoreType = normCore
	profiles.Behavior = behavior
	profiles.Profiles[normCore] = configToProfileItem(config)
	return s.writeProfilesLocked(profiles)
}

func (s *CoreService) writeProfilesLocked(profiles persistedCoreProfiles) error {
	profiles.SchemaVersion = currentSchemaVersion
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		debugLogf("core", "marshal profiles failed: %v", err)
		return err
	}
	data = append(data, '\n')
	configPath := s.configPath()

	// 写入前安全轮转备份现有有效文件
	if existingData, readErr := os.ReadFile(configPath); readErr == nil && len(existingData) > 0 {
		_ = os.WriteFile(s.configBackupPath(), existingData, 0o600)
	}

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

func (s *CoreService) configPath() string {
	return filepath.Join(s.executableDir, "profiles.json")
}

func (s *CoreService) configBackupPath() string {
	return filepath.Join(s.executableDir, "profiles.json.bak")
}

func (s *CoreService) configCorruptedPath() string {
	return filepath.Join(s.executableDir, fmt.Sprintf("profiles.json.corrupted.%s", time.Now().Format("20060102150405")))
}

func (s *CoreService) configFilePath(config CoreConfig) string {
	fileName, err := normalizeConfigFileName(config.ConfigFileName, config.CoreType)
	if err != nil {
		fileName = defaultConfigFileName(config.CoreType)
	}
	return filepath.Join(s.coreDirFor(config.CoreType), fileName)
}
