package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

var systemKernel32 = windows.NewLazySystemDLL("kernel32.dll")

func executablePathAndDir() (string, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("locate executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", fmt.Errorf("eval symlinks: %w", err)
	}
	return executable, filepath.Dir(executable), nil
}

func appUserDataDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "zashdesktop")
}

func isSupportedConfigFile(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	return ext == ".json" || ext == ".yaml" || ext == ".yml"
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

func (s *CoreService) backendDebugLogPath() string {
	return filepath.Join(s.executableDir, "debug.log")
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
			return nil, errors.New("command line arguments contain invalid characters")
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
		return nil, errors.New("command line arguments contain unclosed quotes")
	}
	flush()
	return args, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	var renameErr error
	for attempt := 0; attempt < 5; attempt++ {
		renameErr = os.Rename(temporaryPath, path)
		if renameErr == nil {
			return nil
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return renameErr
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
