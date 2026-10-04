package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	ExpectedSHA256      string
	MaxBytes            int64
	Timeout             time.Duration
	ConnectTimeout      time.Duration
	StallInterval       time.Duration
	MinBytesPerInterval int64
	FilePattern         string
	UseGitHubProxy      bool
}

func buildGitHubDownloadURLs(rawURL string, useProxy bool) []string {
	rawURL = strings.TrimSpace(rawURL)
	if !useProxy || (!strings.HasPrefix(rawURL, "https://github.com/") && !strings.HasPrefix(rawURL, "http://github.com/")) {
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
		if !opts.UseGitHubProxy {
			opts.Timeout = 30 * time.Second
		} else {
			opts.Timeout = 3 * time.Minute
		}
	}
	if opts.FilePattern == "" {
		opts.FilePattern = ".download-*.tmp"
	}

	candidates := buildGitHubDownloadURLs(downloadURL, opts.UseGitHubProxy)
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

type progressWriter struct {
	w       io.Writer
	written *int64
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	if n > 0 {
		atomic.AddInt64(pw.written, int64(n))
	}
	return n, err
}

func downloadSingleFile(downloadURL, baseDir string, opts DownloadOptions) (string, error) {
	debugLogf("download", "start downloading: url=%q checksum=%t", downloadURL, opts.ExpectedSHA256 != "")

	connectTimeout := opts.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 6 * time.Second
	}
	stallInterval := opts.StallInterval
	if stallInterval <= 0 {
		stallInterval = 5 * time.Second
	}
	minBytesPerInterval := opts.MinBytesPerInterval
	if minBytesPerInterval <= 0 {
		minBytesPerInterval = 256 * 1024 // 统一要求至少 256KB/5s (约 50KB/s)，达不到就迅速掐断换下一个加速源！
	}

	singleTimeout := opts.Timeout
	if singleTimeout <= 0 || singleTimeout > 35*time.Second {
		singleTimeout = 35 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), singleTimeout)
	defer cancel()

	var (
		watchdogMu  sync.Mutex
		watchdogErr error
	)

	setWatchdogErr := func(err error) {
		watchdogMu.Lock()
		if watchdogErr == nil {
			watchdogErr = err
		}
		watchdogMu.Unlock()
	}

	getWatchdogErr := func() error {
		watchdogMu.Lock()
		defer watchdogMu.Unlock()
		return watchdogErr
	}

	// 初始阶段：首包/建连响应看门狗
	var connected atomic.Bool
	initialTimer := time.AfterFunc(connectTimeout, func() {
		if !connected.Load() {
			setWatchdogErr(fmt.Errorf("connection timed out (no response within %v)", connectTimeout))
			cancel()
		}
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		connected.Store(true)
		initialTimer.Stop()
		return "", fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", "zashdesktop")

	client := newCoreHTTPClient(0)

	resp, err := client.Do(req)
	connected.Store(true)
	initialTimer.Stop()

	if err != nil {
		if wErr := getWatchdogErr(); wErr != nil {
			return "", wErr
		}
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

	var targetWriter io.Writer = temp
	digest := sha256.New()
	if opts.ExpectedSHA256 != "" {
		targetWriter = io.MultiWriter(temp, digest)
	}

	var writtenTotal int64
	writer := &progressWriter{
		w:       targetWriter,
		written: &writtenTotal,
	}

	// 传输流停滞 / 速率监测看门狗
	stopWatchdog := make(chan struct{})
	defer close(stopWatchdog)

	go func() {
		ticker := time.NewTicker(stallInterval)
		defer ticker.Stop()

		var lastBytes int64
		for {
			select {
			case <-stopWatchdog:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentBytes := atomic.LoadInt64(&writtenTotal)
				delta := currentBytes - lastBytes
				if delta < minBytesPerInterval {
					debugLogf("download", "download stalled/slow: %q only received %d bytes in %v (threshold: %d bytes)", downloadURL, delta, stallInterval, minBytesPerInterval)
					setWatchdogErr(fmt.Errorf("download stalled (only %d KB in %v, expected at least %d KB)", delta/1024, stallInterval, minBytesPerInterval/1024))
					cancel()
					return
				}
				lastBytes = currentBytes
			}
		}
	}()

	written, err := io.Copy(writer, io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil || written > opts.MaxBytes {
		if wErr := getWatchdogErr(); wErr != nil {
			return "", wErr
		}
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
