package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	githubDownloadProxies = []string{
		"https://v6.gh-proxy.org",
		"https://v4.gh-proxy.org",
		"https://gh-proxy.com",
		"https://ghfast.top",
	}
)

type DownloadOptions struct {
	ExpectedSHA256 string
	MaxBytes       int64
	Timeout        time.Duration
	FilePattern    string
}

func buildGitHubDownloadURLs(rawURL string) []string {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "https://github.com/") && !strings.HasPrefix(rawURL, "http://github.com/") {
		return []string{rawURL}
	}
	urls := make([]string, 0, 1+len(githubDownloadProxies))
	for _, proxy := range githubDownloadProxies {
		urls = append(urls, strings.TrimRight(proxy, "/")+"/"+rawURL)
	}
	urls = append(urls, rawURL)
	return urls
}

func downloadFile(downloadURL, baseDir string, opts DownloadOptions) (string, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 300 << 20
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Minute
	}
	if opts.FilePattern == "" {
		opts.FilePattern = ".download-*.tmp"
	}

	candidates := buildGitHubDownloadURLs(downloadURL)
	var lastErr error
	for _, candidate := range candidates {
		filePath, err := downloadSingleFile(candidate, baseDir, opts)
		if err == nil {
			return filePath, nil
		}
		debugLogf("download", "candidate download failed: url=%q err=%v", candidate, err)
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("download failed from all sources")
}

func downloadSingleFile(downloadURL, baseDir string, opts DownloadOptions) (string, error) {
	debugLogf("download", "start downloading: url=%q checksum=%t", downloadURL, opts.ExpectedSHA256 != "")
	client := newCoreHTTPClient(opts.Timeout)

	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", "zashdesktop")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download server returned %s", resp.Status)
	}
	if resp.ContentLength > opts.MaxBytes {
		return "", errors.New("file is too large")
	}

	temp, err := os.CreateTemp(baseDir, opts.FilePattern)
	if err != nil {
		return "", fmt.Errorf("create temp file in %q: %w", baseDir, err)
	}
	filePath := temp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = temp.Close()
			_ = os.Remove(filePath)
		}
	}()

	var writer io.Writer = temp
	digest := sha256.New()
	if opts.ExpectedSHA256 != "" {
		writer = io.MultiWriter(temp, digest)
	}

	written, err := io.Copy(writer, io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil || written > opts.MaxBytes {
		if err != nil {
			return "", fmt.Errorf("save download file: %w", err)
		}
		return "", errors.New("file exceeded maximum allowed size")
	}
	if written == 0 {
		return "", errors.New("downloaded file is empty")
	}

	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close download file: %w", err)
	}

	if opts.ExpectedSHA256 != "" {
		actualSHA256 := hex.EncodeToString(digest.Sum(nil))
		if !strings.EqualFold(actualSHA256, opts.ExpectedSHA256) {
			debugLogf("download", "checksum mismatch: expected=%s actual=%s", opts.ExpectedSHA256, actualSHA256)
			return "", fmt.Errorf("checksum mismatch: expected %s, got %s", opts.ExpectedSHA256, actualSHA256)
		}
	}

	keepTemp = true
	debugLogf("download", "download complete: path=%q bytes=%d", filePath, written)
	return filePath, nil
}
