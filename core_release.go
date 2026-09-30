package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Core Download & Upgrade Methods
// -----------------------------------------------------------------------------

func (s *CoreService) DownloadCore(rawURL, rawCoreType string) (CoreConfig, error) {
	coreDebugf("download request: rawURL=%q coreType=%q", rawURL, rawCoreType)
	coreType, err := normalizeCoreType(rawCoreType)
	if err != nil {
		return CoreConfig{}, err
	}
	config, _, err := s.loadConfigSnapshot(coreType)
	if err != nil {
		return CoreConfig{}, err
	}

	config, archivePath, targetVersion, err := s.downloadCoreArchive(rawURL, config)
	if err != nil {
		coreDebugf("download request failed: err=%v", err)
		return CoreConfig{}, err
	}
	defer os.Remove(archivePath)

	return s.applyCoreUpgrade(coreType, archivePath, targetVersion)
}

func (s *CoreService) downloadCoreArchive(rawURL string, config CoreConfig) (CoreConfig, string, string, error) {
	s.applyCurrentVersion(&config, "")

	downloadURLTemplate := strings.TrimSpace(rawURL)
	if downloadURLTemplate == "" {
		downloadURLTemplate = defaultCoreURLTemplate(config.CoreType, config.Channel)
	}

	targetVersion := normalizeCoreVersion(config.LatestVersion)
	if isMihomoTestPlaceholderVersion(config, targetVersion) {
		targetVersion = ""
	}

	expectedSHA256 := ""
	owner, repo := parseGitHubRepo(downloadURLTemplate)
	if cached, ok := s.getCachedLatestRelease(owner, repo, config.Channel); ok {
		if targetVersion == "" {
			targetVersion = cached.version
		}
		expectedSHA256 = cached.digest
	}

	if targetVersion == "" {
		var err error
		targetVersion, expectedSHA256, err = findLatestReleaseForURL(downloadURLTemplate, config.Channel)
		if err != nil {
			return CoreConfig{}, "", "", err
		}
		s.setCachedLatestRelease(owner, repo, config.Channel, targetVersion, expectedSHA256)
	}
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" {
		return CoreConfig{}, "", "", errors.New("unable to determine core version to download")
	}

	downloadURL := strings.ReplaceAll(downloadURLTemplate, "{version}", targetVersion)

	archivePath, err := downloadFile(downloadURL, s.executableDir, DownloadOptions{
		ExpectedSHA256: expectedSHA256,
		MaxBytes:       maxCoreDownload,
		Timeout:        20 * time.Minute,
		FilePattern:    ".core-download-*.zip",
	})
	if err != nil {
		return CoreConfig{}, "", "", err
	}
	return config, archivePath, targetVersion, nil
}

// -----------------------------------------------------------------------------
// Archive Extraction & Binary Replacement
// -----------------------------------------------------------------------------

func isCoreArchiveExecutable(name, coreType string) bool {
	prefix := coreExecutableBaseName
	if normalizedCoreType(coreType) == coreTypeMihomo {
		prefix = mihomoExecutableName
	}
	clean := strings.ToLower(strings.TrimSuffix(filepath.Base(name), ".exe"))
	return clean == prefix || strings.HasPrefix(clean, prefix+"-") || strings.HasPrefix(clean, prefix+"_")
}

func extractAndReplaceCoreExe(archivePath, targetPath, coreType string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open core ZIP archive: %w", err)
	}
	defer archive.Close()

	var selected *zip.File
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() || entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.UncompressedSize64 > uint64(maxCoreBinary) {
			continue
		}
		if isCoreArchiveExecutable(entry.Name, coreType) {
			if selected == nil || strings.Count(entry.Name, "/") < strings.Count(selected.Name, "/") {
				selected = entry
			}
		}
	}
	if selected == nil {
		return fmt.Errorf("core executable not found in ZIP archive for %s", coreType)
	}

	reader, err := selected.Open()
	if err != nil {
		return fmt.Errorf("read core executable from zip: %w", err)
	}
	defer reader.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create core directory: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(targetPath), ".core-executable-*.exe")
	if err != nil {
		return fmt.Errorf("create temp core executable: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()

	written, err := io.Copy(temp, io.LimitReader(reader, maxCoreBinary+1))
	if err != nil || written == 0 || written > maxCoreBinary {
		if err != nil {
			return fmt.Errorf("extract core executable: %w", err)
		}
		return errors.New("core executable is invalid or too large")
	}
	_ = temp.Chmod(0o755)
	if err := temp.Close(); err != nil {
		return err
	}
	temp = nil

	return replaceExecutable(tempPath, targetPath)
}

func replaceExecutable(sourcePath, targetPath string) error {
	defer os.Remove(sourcePath)
	previousPath := targetPath + ".replacing"
	_ = os.Remove(previousPath)
	if fileExists(targetPath) {
		if err := os.Rename(targetPath, previousPath); err != nil {
			if isFileLockedError(err) {
				coreDebugf("target file %q is locked (sharing violation)", targetPath)
				return fmt.Errorf("target core executable is locked: %w", err)
			}
			coreDebugf("backup target file %q failed: %v", targetPath, err)
			return fmt.Errorf("prepare core replacement: %w", err)
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		coreDebugf("move source file %q to %q failed: %v", sourcePath, targetPath, err)
		if fileExists(previousPath) {
			_ = os.Rename(previousPath, targetPath)
		}
		return fmt.Errorf("replace core executable: %w", err)
	}
	_ = os.Remove(previousPath)
	coreDebugf("successfully replaced core binary at %q", targetPath)
	return nil
}
