package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	semverPattern   = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])v?(\d+\.\d+\.\d+(-[0-9a-z]+([.-][0-9a-z]+)*)?)(?:[^0-9a-z]|$)`)
	buildVerPattern = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])v?((?:alpha|beta|rc|dev|nightly|preview)(?:[-._][0-9a-z]+)*)(?:[^0-9a-z]|$)`)
	testVerPattern  = regexp.MustCompile(`(?i)^(?:alpha|alpha-smart|beta|dev|rc|nightly|preview)(?:[-._][0-9a-z]+)*$`)
	testChanPattern = regexp.MustCompile(`(?i)(^|[-._])(alpha|beta|rc|dev|nightly|preview)([-._]|\d|$)`)
)

// -----------------------------------------------------------------------------
// Core Version Cache
// -----------------------------------------------------------------------------

func (s *CoreService) getCachedCoreVersion(coreType, channel string) (coreVersionCacheItem, bool) {
	s.versionCacheMu.RLock()
	defer s.versionCacheMu.RUnlock()
	if s.versionCache == nil {
		return coreVersionCacheItem{}, false
	}
	key := normalizedCoreType(coreType) + ":" + strings.ToLower(strings.TrimSpace(channel))
	item, ok := s.versionCache[key]
	return item, ok
}

func (s *CoreService) setCachedCoreVersion(coreType, channel string, item coreVersionCacheItem) {
	s.versionCacheMu.Lock()
	defer s.versionCacheMu.Unlock()
	if s.versionCache == nil {
		s.versionCache = make(map[string]coreVersionCacheItem)
	}
	key := normalizedCoreType(coreType) + ":" + strings.ToLower(strings.TrimSpace(channel))
	s.versionCache[key] = item
}

// -----------------------------------------------------------------------------
// Local Core Version Reading & Probing
// -----------------------------------------------------------------------------

func (s *CoreService) applyCurrentVersion(config *CoreConfig, supplied string) {
	if config.Channel == "" {
		config.Channel = coreChannelStable
	}
	suppliedVersion := normalizeCoreVersion(supplied)
	corePath := s.corePathFor(config.CoreType, config.Channel)
	config.CorePath = corePath

	stat, err := os.Stat(corePath)
	if err != nil || stat.IsDir() {
		config.Installed = false
		config.InstalledVersion = ""
		config.Version = suppliedVersion
		return
	}

	config.Installed = true
	if suppliedVersion != "" {
		config.Version = suppliedVersion
		config.InstalledVersion = suppliedVersion
		return
	}

	if cached, ok := s.getCachedCoreVersion(config.CoreType, config.Channel); ok && cached.modTime.Equal(stat.ModTime()) && cached.size == stat.Size() {
		if cached.version != "" {
			config.Version = cached.version
			config.VersionDetail = cached.detail
			config.InstalledVersion = cached.version
		} else if config.InstalledVersion != "" {
			config.Version = config.InstalledVersion
		}
		return
	}

	version, versionDetail, err := readCoreVersionDetail(corePath, config.CoreType)
	s.setCachedCoreVersion(config.CoreType, config.Channel, coreVersionCacheItem{
		modTime: stat.ModTime(),
		size:    stat.Size(),
		version: version,
		detail:  versionDetail,
	})
	if err == nil && version != "" {
		config.Version = version
		config.VersionDetail = versionDetail
		config.InstalledVersion = version
	} else if config.InstalledVersion != "" {
		config.Version = config.InstalledVersion
	}
}

func readCoreVersionDetail(corePath, coreType string) (string, string, error) {
	if !fileExists(corePath) {
		return "", "", fmt.Errorf("%s core is not installed", coreType)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	versionArgs := []string{"version"}
	if normalizedCoreType(coreType) == coreTypeMihomo {
		versionArgs = []string{"-v"}
	}
	cmd := exec.CommandContext(ctx, corePath, versionArgs...)
	configureCoreCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		debugLogf("release", "execute %s %v failed: %v", corePath, versionArgs, err)
		return "", "", fmt.Errorf("read %s core version: %w", coreType, err)
	}
	version := normalizeCoreVersion(string(output))
	if version == "" {
		debugLogf("release", "unable to parse version from output: %q", string(output))
		return "", "", fmt.Errorf("unable to read %s core version", coreType)
	}
	return version, strings.TrimSpace(string(output)), nil
}

// -----------------------------------------------------------------------------
// Version Parsing, Normalization & Comparison
// -----------------------------------------------------------------------------

type coreVersion struct {
	major     int
	minor     int
	patch     int
	hasSemver bool
	suffix    []string
}

func stripArchiveExtension(v string) string {
	lower := strings.ToLower(v)
	for _, ext := range []string{".tar.gz", ".tar.xz", ".zip", ".tgz", ".gz", ".exe"} {
		if strings.HasSuffix(lower, ext) {
			return v[:len(v)-len(ext)]
		}
	}
	return v
}

func normalizeCoreVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	clean := stripArchiveExtension(value)
	if match := semverPattern.FindStringSubmatch(clean); len(match) >= 2 && match[1] != "" {
		return match[1]
	}
	if match := buildVerPattern.FindStringSubmatch(clean); len(match) >= 2 && match[1] != "" {
		return match[1]
	}
	return ""
}

func parseCoreVersionParts(value string) (coreVersion, error) {
	version := normalizeCoreVersion(value)
	if version == "" {
		return coreVersion{}, fmt.Errorf("unsupported version %q", value)
	}
	base := version
	var suffix []string
	if idx := strings.IndexByte(version, '-'); idx >= 0 {
		base = version[:idx]
		for _, part := range strings.FieldsFunc(version[idx+1:], func(r rune) bool { return r == '.' || r == '-' }) {
			if part != "" {
				suffix = append(suffix, part)
			}
		}
	}
	var parsed coreVersion
	n, _ := fmt.Sscanf(base, "%d.%d.%d", &parsed.major, &parsed.minor, &parsed.patch)
	if n >= 2 {
		parsed.hasSemver = true
		parsed.suffix = suffix
		return parsed, nil
	}
	parsed.hasSemver = false
	parsed.suffix = []string{strings.ToLower(version)}
	return parsed, nil
}

func mustParseCoreVersion(value string) coreVersion {
	parsed, _ := parseCoreVersionParts(value)
	return parsed
}

func compareCoreVersions(left, right coreVersion) int {
	for _, pair := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.suffix) == 0 && len(right.suffix) > 0 {
		return 1
	}
	if len(left.suffix) > 0 && len(right.suffix) == 0 {
		return -1
	}
	for i := 0; i < len(left.suffix) && i < len(right.suffix); i++ {
		lPart, rPart := left.suffix[i], right.suffix[i]
		lNum, lErr := strconv.Atoi(lPart)
		rNum, rErr := strconv.Atoi(rPart)
		if lErr == nil && rErr == nil {
			if lNum < rNum {
				return -1
			}
			if lNum > rNum {
				return 1
			}
			continue
		}
		if lErr == nil && rErr != nil {
			return -1
		}
		if lErr != nil && rErr == nil {
			return 1
		}
		if strings.ToLower(lPart) < strings.ToLower(rPart) {
			return -1
		}
		if strings.ToLower(lPart) > strings.ToLower(rPart) {
			return 1
		}
	}
	if len(left.suffix) < len(right.suffix) {
		return -1
	}
	if len(left.suffix) > len(right.suffix) {
		return 1
	}
	return 0
}

func isCoreUpdateAvailable(latest, current, channel string) bool {
	latest = strings.TrimSpace(latest)
	current = strings.TrimSpace(current)
	if latest == "" {
		return false
	}
	if current == "" {
		return true
	}
	if strings.EqualFold(latest, current) {
		return false
	}
	pLatest, lErr := parseCoreVersionParts(latest)
	pCurrent, cErr := parseCoreVersionParts(current)
	if lErr == nil && cErr == nil && pLatest.hasSemver && pCurrent.hasSemver {
		return compareCoreVersions(pLatest, pCurrent) > 0
	}
	return !strings.EqualFold(latest, current)
}

func coreChannel(version string) string {
	if testChanPattern.MatchString(version) {
		return coreChannelTest
	}
	return coreChannelStable
}

func normalizeCoreChannel(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case coreChannelStable:
		return coreChannelStable, nil
	case coreChannelTest:
		return coreChannelTest, nil
	default:
		return "", errors.New("core channel must be stable or test")
	}
}
