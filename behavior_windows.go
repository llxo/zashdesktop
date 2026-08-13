//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	behaviorLayersKey = `Software\Microsoft\Windows NT\CurrentVersion\AppCompatFlags\Layers`
	autoStartTaskName = "sing-box-gui"
)

func readRunAsAdminSetting(applicationPath string) (bool, error) {
	if strings.TrimSpace(applicationPath) == "" {
		return false, nil
	}

	key, err := registry.OpenKey(registry.CURRENT_USER, behaviorLayersKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open administrator compatibility settings: %w", err)
	}
	defer key.Close()

	value, _, err := key.GetStringValue(applicationPath)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read administrator compatibility setting: %w", err)
	}
	for _, item := range strings.Fields(value) {
		if strings.EqualFold(item, "RunAsAdmin") {
			return true, nil
		}
	}
	return false, nil
}

func writeRunAsAdminSetting(applicationPath string, enabled bool) error {
	if strings.TrimSpace(applicationPath) == "" {
		return errors.New("application path is empty")
	}

	if enabled {
		key, _, err := registry.CreateKey(registry.CURRENT_USER, behaviorLayersKey, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("create administrator compatibility settings: %w", err)
		}
		defer key.Close()
		if err := key.SetStringValue(applicationPath, "RunAsAdmin"); err != nil {
			return fmt.Errorf("enable administrator mode: %w", err)
		}
		return nil
	}

	key, err := registry.OpenKey(registry.CURRENT_USER, behaviorLayersKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open administrator compatibility settings: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(applicationPath); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("disable administrator mode: %w", err)
	}
	return nil
}

func readAutoStartSetting() (bool, error) {
	command := exec.Command("schtasks.exe", "/Query", "/TN", autoStartTaskName)
	configureCoreCommand(command)
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return false, nil
		}
		return false, fmt.Errorf("query startup task: %w", err)
	}
	return true, nil
}

func writeAutoStartSetting(applicationPath string, enabled bool) error {
	if strings.TrimSpace(applicationPath) == "" {
		return errors.New("application path is empty")
	}

	var args []string
	if enabled {
		args = []string{
			"/Create",
			"/TN", autoStartTaskName,
			"/TR", fmt.Sprintf(`"%s" --start-hidden`, applicationPath),
			"/SC", "ONLOGON",
			"/F",
		}
	} else {
		args = []string{"/Delete", "/TN", autoStartTaskName, "/F"}
	}

	command := exec.Command("schtasks.exe", args...)
	configureCoreCommand(command)
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if !enabled && errors.As(err, &exitError) {
			return nil
		}
		return fmt.Errorf("%s startup task: %w", map[bool]string{true: "enable", false: "disable"}[enabled], err)
	}
	return nil
}
