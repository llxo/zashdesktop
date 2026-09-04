package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

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
