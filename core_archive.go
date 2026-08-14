package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
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

type coreArchiveTools struct {
	baseDir     string
	maxDownload int64
	maxBinary   int64
	logf        func(string, ...any)
}

func newCoreHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = systemProxy
	return &http.Client{Timeout: timeout, Transport: transport}
}

func (t coreArchiveTools) debugf(format string, args ...any) {
	if t.logf != nil {
		t.logf(format, args...)
	}
}

func (t coreArchiveTools) Download(downloadURL, expectedSHA256 string) (string, error) {
	t.debugf("HTTP download start: url=%q checksum=%t", downloadURL, expectedSHA256 != "")
	client := newCoreHTTPClient(20 * time.Minute)
	response, err := client.Get(downloadURL)
	if err != nil {
		t.debugf("HTTP download request failed: url=%q err=%v", downloadURL, err)
		return "", fmt.Errorf("download core: %w", err)
	}
	defer response.Body.Close()
	t.debugf("HTTP download response: status=%s contentLength=%d", response.Status, response.ContentLength)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.debugf("HTTP download rejected: status=%s", response.Status)
		return "", fmt.Errorf("download core: server returned %s", response.Status)
	}
	if response.ContentLength > t.maxDownload {
		t.debugf("HTTP download rejected: contentLength=%d limit=%d", response.ContentLength, t.maxDownload)
		return "", errors.New("core archive is too large")
	}

	suffix := ".archive"
	if parsed, parseErr := url.Parse(downloadURL); parseErr == nil {
		lowerPath := strings.ToLower(parsed.Path)
		switch {
		case strings.HasSuffix(lowerPath, ".tar.gz"):
			suffix = ".tar.gz"
		case strings.HasSuffix(lowerPath, ".zip"):
			suffix = ".zip"
		}
	}
	temporary, err := os.CreateTemp(t.baseDir, ".core-download-*"+suffix)
	if err != nil {
		t.debugf("create archive temp file failed: suffix=%q err=%v", suffix, err)
		return "", fmt.Errorf("create core archive: %w", err)
	}
	path := temporary.Name()
	t.debugf("archive temp file created: path=%q format=%s", path, suffix)
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
	}()

	var writer io.Writer = temporary
	digest := sha256.New()
	if expectedSHA256 != "" {
		writer = io.MultiWriter(temporary, digest)
	}
	written, err := io.Copy(writer, io.LimitReader(response.Body, t.maxDownload+1))
	if err != nil {
		t.debugf("save downloaded archive failed: path=%q bytes=%d err=%v", path, written, err)
		_ = temporary.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("save core archive: %w", err)
	}
	if written > t.maxDownload {
		t.debugf("downloaded archive exceeded limit: path=%q bytes=%d limit=%d", path, written, t.maxDownload)
		_ = temporary.Close()
		_ = os.Remove(path)
		return "", errors.New("core archive is too large")
	}
	if err := temporary.Close(); err != nil {
		t.debugf("close archive temp file failed: path=%q err=%v", path, err)
		_ = os.Remove(path)
		return "", fmt.Errorf("close core archive: %w", err)
	}
	if expectedSHA256 != "" {
		actualSHA256 := fmt.Sprintf("%x", digest.Sum(nil))
		if !strings.EqualFold(actualSHA256, expectedSHA256) {
			t.debugf("archive checksum mismatch: path=%q expected=%s actual=%s", path, expectedSHA256, actualSHA256)
			_ = os.Remove(path)
			return "", errors.New("core archive checksum does not match the release digest")
		}
		t.debugf("archive checksum verified: path=%q sha256=%s", path, actualSHA256)
	}
	temporary = nil
	t.debugf("HTTP download complete: path=%q bytes=%d", path, written)
	return path, nil
}

func (t coreArchiveTools) Extract(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	t.debugf("extract archive start: archive=%q target=%q", archivePath, targetPath)
	if isExecutable == nil {
		return false, errors.New("archive executable matcher is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.debugf("create core directory failed: path=%q err=%v", filepath.Dir(targetPath), err)
		return false, fmt.Errorf("create core directory: %w", err)
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz") {
		t.debugf("extract archive format: tar.gz")
		return t.extractTarGZ(archivePath, targetPath, isExecutable)
	}
	t.debugf("extract archive format: zip")
	return t.extractZIP(archivePath, targetPath, isExecutable)
}

func (t coreArchiveTools) extractZIP(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.debugf("open ZIP archive failed: archive=%q err=%v", archivePath, err)
		return false, fmt.Errorf("open core ZIP archive: %w", err)
	}
	defer archive.Close()
	t.debugf("ZIP archive opened: archive=%q entries=%d", archivePath, len(archive.File))

	var selected *zip.File
	for _, entry := range archive.File {
		name := filepath.Base(strings.ReplaceAll(entry.Name, "\\", "/"))
		if !isExecutable(name) || entry.FileInfo().IsDir() || entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.UncompressedSize64 > uint64(t.maxBinary) {
			continue
		}
		if selected == nil || strings.Count(entry.Name, "/") < strings.Count(selected.Name, "/") {
			selected = entry
		}
	}
	if selected == nil {
		t.debugf("ZIP executable not found: archive=%q", archivePath)
		return false, errors.New("core executable was not found in the ZIP archive")
	}
	t.debugf("ZIP executable selected: entry=%q size=%d", selected.Name, selected.UncompressedSize64)

	reader, err := selected.Open()
	if err != nil {
		t.debugf("open ZIP executable failed: entry=%q err=%v", selected.Name, err)
		return false, fmt.Errorf("read core executable: %w", err)
	}
	defer reader.Close()
	return t.installExecutable(reader, targetPath)
}

func (t coreArchiveTools) extractTarGZ(archivePath, targetPath string, isExecutable func(string) bool) (bool, error) {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.debugf("open TAR.GZ archive failed: archive=%q err=%v", archivePath, err)
		return false, fmt.Errorf("open core TAR.GZ archive: %w", err)
	}
	defer archiveFile.Close()

	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		t.debugf("open gzip stream failed: archive=%q err=%v", archivePath, err)
		return false, fmt.Errorf("open core gzip archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.debugf("read TAR archive failed: archive=%q err=%v", archivePath, nextErr)
			return false, fmt.Errorf("read core TAR archive: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > t.maxBinary || !isExecutable(filepath.Base(header.Name)) {
			continue
		}
		t.debugf("TAR executable selected: entry=%q size=%d", header.Name, header.Size)
		return t.installExecutable(io.LimitReader(tarReader, header.Size), targetPath)
	}
	t.debugf("TAR executable not found: archive=%q", archivePath)
	return false, errors.New("core executable was not found in the TAR.GZ archive")
}

func (t coreArchiveTools) installExecutable(reader io.Reader, targetPath string) (bool, error) {
	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".core-executable-*"+filepath.Ext(targetPath))
	if err != nil {
		t.debugf("create extracted executable temp file failed: target=%q err=%v", targetPath, err)
		return false, fmt.Errorf("create core file: %w", err)
	}
	temporaryPath := temporary.Name()
	t.debugf("extracted executable temp file created: path=%q target=%q", temporaryPath, targetPath)
	defer os.Remove(temporaryPath)

	written, err := io.Copy(temporary, io.LimitReader(reader, t.maxBinary+1))
	if err != nil {
		t.debugf("write extracted executable failed: path=%q bytes=%d err=%v", temporaryPath, written, err)
		_ = temporary.Close()
		return false, fmt.Errorf("extract core executable: %w", err)
	}
	if written == 0 || written > t.maxBinary {
		t.debugf("extracted executable size invalid: path=%q bytes=%d limit=%d", temporaryPath, written, t.maxBinary)
		_ = temporary.Close()
		return false, errors.New("core executable is invalid or too large")
	}
	if err := temporary.Chmod(0o755); err != nil {
		t.debugf("set extracted executable permissions failed: path=%q err=%v", temporaryPath, err)
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		t.debugf("close extracted executable temp file failed: path=%q err=%v", temporaryPath, err)
		return false, err
	}

	t.debugf("extracted executable ready for replacement: path=%q bytes=%d", temporaryPath, written)
	installed, err := t.replaceExecutable(temporaryPath, targetPath)
	if err != nil {
		t.debugf("executable replacement failed: source=%q target=%q err=%v", temporaryPath, targetPath, err)
		return false, err
	}
	if installed {
		t.debugf("executable replacement succeeded: target=%q", targetPath)
	} else {
		t.debugf("executable replacement deferred: target=%q", targetPath)
	}
	return installed, nil
}

func (t coreArchiveTools) replaceExecutable(sourcePath, targetPath string) (bool, error) {
	previousPath := targetPath + ".replacing"
	t.debugf("replace executable start: source=%q target=%q", sourcePath, targetPath)
	_ = os.Remove(previousPath)
	if fileExists(targetPath) {
		t.debugf("existing executable found, moving aside: path=%q backup=%q", targetPath, previousPath)
		if err := os.Rename(targetPath, previousPath); err != nil {
			if isFileLockedError(err) {
				t.debugf("existing executable is locked: path=%q", targetPath)
				return false, nil
			}
			t.debugf("move existing executable aside failed: path=%q err=%v", targetPath, err)
			return false, fmt.Errorf("prepare core replacement: %w", err)
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		t.debugf("move new executable into place failed: source=%q target=%q err=%v", sourcePath, targetPath, err)
		if fileExists(previousPath) {
			if restoreErr := os.Rename(previousPath, targetPath); restoreErr != nil {
				t.debugf("restore previous executable failed: backup=%q target=%q err=%v", previousPath, targetPath, restoreErr)
			} else {
				t.debugf("previous executable restored: target=%q", targetPath)
			}
		}
		return false, fmt.Errorf("replace core executable: %w", err)
	}
	_ = os.Remove(previousPath)
	t.debugf("replace executable complete: target=%q", targetPath)
	return true, nil
}
