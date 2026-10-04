package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	owner, repo := parseGitHubRepo(downloadURLTemplate)
	if cached, ok := s.getCachedLatestRelease(owner, repo, config.Channel); ok {
		if targetVersion == "" {
			targetVersion = cached.version
		}
	}

	if targetVersion == "" {
		var err error
		targetVersion, _, err = findLatestReleaseForURL(downloadURLTemplate, config.Channel)
		if err != nil {
			return CoreConfig{}, "", "", err
		}
		s.setCachedLatestRelease(owner, repo, config.Channel, targetVersion, "")
	}
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" {
		return CoreConfig{}, "", "", errors.New("unable to determine core version to download")
	}

	downloadURL := strings.ReplaceAll(downloadURLTemplate, "{version}", targetVersion)
	targetFile := filepath.Base(downloadURL)

	tag := targetVersion
	if config.Channel == coreChannelTest && normalizedCoreType(config.CoreType) == coreTypeMihomo {
		tag = mihomoPrereleaseTag
	}
	expectedSHA256 := fetchReleaseAssetDigest(owner, repo, tag, targetFile)

	archivePath, err := downloadFile(downloadURL, s.executableDir, DownloadOptions{
		ExpectedSHA256: expectedSHA256,
		MaxBytes:       maxCoreDownload,
		FilePattern:    ".core-download-*.zip",
		UseGitHubProxy: s.isGitHubProxyEnabled(),
	})
	if err != nil {
		if !s.isGitHubProxyEnabled() && !strings.Contains(err.Error(), "checksum mismatch") {
			return CoreConfig{}, "", "", fmt.Errorf("%w (githubProxyDisabled)", err)
		}
		return CoreConfig{}, "", "", err
	}
	return config, archivePath, targetVersion, nil
}

// -----------------------------------------------------------------------------
// Archive Extraction & Binary Replacement
// -----------------------------------------------------------------------------

func fetchReleaseAssetDigest(owner, repo, tag, targetFilename string) string {
	if owner == "" || repo == "" || tag == "" {
		return ""
	}

	tags := []string{tag}
	trimmed := strings.TrimPrefix(tag, "v")
	if trimmed != tag {
		tags = append(tags, trimmed)
	} else {
		tags = append(tags, "v"+tag)
	}

	client := newCoreHTTPClient(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, t := range tags {
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(t))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "zashdesktop")
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := client.Do(req)
		if err != nil {
			debugLogf("release", "fetch asset digest HTTP failed for %s: %v", endpoint, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var payload struct {
			Assets []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			} `json:"assets"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			continue
		}

		// 严格精确匹配目标文件名
		for _, a := range payload.Assets {
			if strings.EqualFold(a.Name, targetFilename) {
				if strings.HasPrefix(strings.ToLower(a.Digest), "sha256:") {
					digest := strings.TrimSpace(a.Digest[7:])
					debugLogf("release", "asset digest exact match: %s -> sha256:%s", a.Name, digest)
					return digest
				}
			}
		}
	}

	debugLogf("release", "unable to resolve asset digest for %s/%s@%s (target=%s), proceeding without checksum", owner, repo, tag, targetFilename)
	return ""
}

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
