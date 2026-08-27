package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	singboxConfigArgPattern = regexp.MustCompile(`(?i)(^|\s)-c\s+([^\s]+)`)
	mihomoConfigArgPattern  = regexp.MustCompile(`(?i)(^|\s)-f\s+([^\s]+)`)
)

type deletedConfigFile struct {
	CoreType string
	FileName string
	Content  []byte
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

func (s *CoreService) ListConfigFiles(rawCoreType string) ([]string, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.coreDirFor(coreType)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read core directory: %w", err)
	}

	var files []string
	defaultName := defaultConfigFileName(coreType)
	hasDefault := false

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}
		if name == defaultName {
			hasDefault = true
			continue
		}
		files = append(files, name)
	}

	sort.Strings(files)
	if hasDefault {
		files = append([]string{defaultName}, files...)
	}

	return files, nil
}

func (s *CoreService) SelectConfigFile(rawFileName, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	fileName := strings.TrimSpace(rawFileName)
	if fileName == "" {
		return CoreConfig{}, errors.New("请选择配置文件")
	}
	if fileName != filepath.Base(fileName) || strings.ContainsAny(fileName, `<>:"/\|?*`) {
		return CoreConfig{}, errors.New("配置文件名无效")
	}

	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != nil && normalizedCoreType(s.processCoreType) == coreType {
		return CoreConfig{}, errors.New("核心运行中，无法修改生效配置")
	}
	if s.externalProcess != nil && normalizedCoreType(s.externalCoreType) == coreType {
		return CoreConfig{}, errors.New("核心运行中，无法修改生效配置")
	}
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}

	config.RunArgs = updateRunArgsWithConfigFile(config.RunArgs, fileName, coreType)
	config.ConfigFileName = fileName

	coreDebugf("select config file: type=%s file=%q", coreType, fileName)
	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) DeleteConfigFile(rawFileName, rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		coreDebugf("delete config file failed: normalize coreType error=%v", err)
		return CoreConfig{}, err
	}
	fileName := strings.TrimSpace(rawFileName)
	if fileName == "" {
		coreDebugf("delete config file failed: empty fileName")
		return CoreConfig{}, errors.New("请选择要删除的配置文件")
	}
	if fileName != filepath.Base(fileName) || strings.ContainsAny(fileName, `<>:"/\|?*`) {
		coreDebugf("delete config file failed: invalid fileName=%q", fileName)
		return CoreConfig{}, errors.New("配置文件名无效")
	}

	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		coreDebugf("delete config file failed: loadConfigSnapshot err=%v", err)
		return CoreConfig{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != nil && normalizedCoreType(s.processCoreType) == coreType {
		coreDebugf("delete config file failed: core is running (managed pid=%d)", s.process.Process.Pid)
		return CoreConfig{}, errors.New("核心运行中，无法删除配置")
	}
	if s.externalProcess != nil && normalizedCoreType(s.externalCoreType) == coreType {
		coreDebugf("delete config file failed: core is running (external pid=%d)", s.externalProcess.Pid)
		return CoreConfig{}, errors.New("核心运行中，无法删除配置")
	}
	if s.configGeneration != generation {
		coreDebugf("delete config file failed: config generation mismatch")
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}

	dir := s.coreDirFor(coreType)
	filePath := filepath.Join(dir, fileName)
	content, readErr := os.ReadFile(filePath)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		coreDebugf("delete config file failed: type=%s name=%q err=%v", coreType, fileName, err)
		return CoreConfig{}, fmt.Errorf("删除配置文件失败: %w", err)
	}

	if s.lastDeletedFiles == nil {
		s.lastDeletedFiles = make(map[string]deletedConfigFile)
	}
	if readErr == nil && len(content) > 0 {
		s.lastDeletedFiles[coreType] = deletedConfigFile{
			CoreType: coreType,
			FileName: fileName,
			Content:  content,
		}
	}
	coreDebugf("delete config file success: type=%s name=%q canUndo=%t", coreType, fileName, readErr == nil && len(content) > 0)

	entries, _ := os.ReadDir(dir)
	var nextFile string
	defaultName := defaultConfigFileName(coreType)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".json" || ext == ".yaml" || ext == ".yml" {
			if name == defaultName {
				nextFile = name
				break
			}
			if nextFile == "" {
				nextFile = name
			}
		}
	}
	if nextFile == "" {
		nextFile = defaultName
	}

	currentActive := fileName
	if match := singboxConfigArgPattern.FindStringSubmatch(config.RunArgs); len(match) > 2 && coreType == coreTypeSingBox {
		currentActive = filepath.Base(match[2])
	} else if match := mihomoConfigArgPattern.FindStringSubmatch(config.RunArgs); len(match) > 2 && coreType == coreTypeMihomo {
		currentActive = filepath.Base(match[2])
	}

	if strings.EqualFold(currentActive, fileName) || strings.EqualFold(config.ConfigFileName, fileName) {
		config.ConfigFileName = nextFile
		config.RunArgs = updateRunArgsWithConfigFile(config.RunArgs, nextFile, coreType)
		if err := s.saveConfigLocked(config); err != nil {
			return CoreConfig{}, err
		}
	}

	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) UndoDeleteConfigFile(rawCoreType string) (CoreConfig, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}

	config, generation, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != nil && normalizedCoreType(s.processCoreType) == coreType {
		return CoreConfig{}, errors.New("核心运行中，无法撤销删除")
	}
	if s.externalProcess != nil && normalizedCoreType(s.externalCoreType) == coreType {
		return CoreConfig{}, errors.New("核心运行中，无法撤销删除")
	}
	if s.configGeneration != generation {
		return CoreConfig{}, errors.New("core configuration changed while saving; please retry")
	}

	if s.lastDeletedFiles == nil {
		return CoreConfig{}, errors.New("没有可撤销删除的配置文件")
	}
	deleted, ok := s.lastDeletedFiles[coreType]
	if !ok || len(deleted.Content) == 0 {
		return CoreConfig{}, errors.New("没有可撤销删除的配置文件")
	}

	dir := s.coreDirFor(coreType)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		coreDebugf("undo delete create dir failed: type=%s err=%v", coreType, err)
		return CoreConfig{}, fmt.Errorf("创建核心目录失败: %w", err)
	}
	filePath := filepath.Join(dir, deleted.FileName)
	if err := writeFileAtomically(filePath, deleted.Content, 0o600); err != nil {
		coreDebugf("undo delete write file failed: type=%s name=%q err=%v", coreType, deleted.FileName, err)
		return CoreConfig{}, fmt.Errorf("恢复配置文件失败: %w", err)
	}
	delete(s.lastDeletedFiles, coreType)
	coreDebugf("undo delete success: type=%s name=%q", coreType, deleted.FileName)

	config.ConfigFileName = deleted.FileName
	config.RunArgs = updateRunArgsWithConfigFile(config.RunArgs, deleted.FileName, coreType)

	if err := s.saveConfigLocked(config); err != nil {
		return CoreConfig{}, err
	}
	s.applyRuntimeState(&config)
	return config, nil
}

func (s *CoreService) CanUndoDeleteConfigFile(rawCoreType string) (bool, error) {
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastDeletedFiles == nil {
		return false, nil
	}
	_, ok := s.lastDeletedFiles[coreType]
	return ok, nil
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

func updateRunArgsWithConfigFile(currentArgs, fileName, coreType string) string {
	fileName = filepath.Base(fileName)
	trimmed := strings.TrimSpace(currentArgs)
	if normalizedCoreType(coreType) == coreTypeMihomo {
		if trimmed == "" || isDefaultCoreRunArgs(trimmed) {
			return fmt.Sprintf("-d . -f %s", fileName)
		}
		if mihomoConfigArgPattern.MatchString(trimmed) {
			return mihomoConfigArgPattern.ReplaceAllString(trimmed, fmt.Sprintf("${1}-f %s", fileName))
		}
		return fmt.Sprintf("%s -f %s", trimmed, fileName)
	}

	if trimmed == "" || isDefaultCoreRunArgs(trimmed) {
		return fmt.Sprintf("run -c %s -D .", fileName)
	}
	if singboxConfigArgPattern.MatchString(trimmed) {
		return singboxConfigArgPattern.ReplaceAllString(trimmed, fmt.Sprintf("${1}-c %s", fileName))
	}
	return fmt.Sprintf("%s -c %s", trimmed, fileName)
}

func isDefaultCoreRunArgs(raw string) bool {
	runArgs := strings.TrimSpace(raw)
	return runArgs == defaultCoreRunArgs || runArgs == defaultMihomoRunArgs
}
