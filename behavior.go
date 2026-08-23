package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	behaviorLayersKey = `Software\Microsoft\Windows NT\CurrentVersion\AppCompatFlags\Layers`
	autoStartTaskName = "zashdesktop"
	autoStartDelay    = 30
)

var (
	behaviorAdvapi32             = windows.NewLazySystemDLL("advapi32.dll")
	behaviorCheckTokenMembership = behaviorAdvapi32.NewProc("CheckTokenMembership")
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

func autoStartTaskXML(applicationPath string) []byte {
	configuration := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>%[1]s at startup</Description>
    <URI>\%[1]s</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Delay>PT%[2]dS</Delay>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>true</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT72H</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%[3]s</Command>
      <Arguments>--start-hidden</Arguments>
    </Exec>
  </Actions>
</Task>`, autoStartTaskName, autoStartDelay, applicationPath)
	return []byte(configuration)
}

func isPrivileged() (bool, error) {
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}

	var isMember int32
	ret, _, err := behaviorCheckTokenMembership.Call(0, uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&isMember)))
	if ret == 0 {
		return false, err
	}
	return isMember != 0, nil
}

func runAutoStartCommand(args []string) error {
	privileged, err := isPrivileged()
	if err != nil {
		return err
	}

	var command *exec.Cmd
	if privileged {
		command = exec.Command("SchTasks", args...)
	} else {
		quotedArgs := make([]string, len(args))
		for index, arg := range args {
			quotedArgs[index] = `"` + strings.ReplaceAll(arg, `"`, `""`) + `"`
		}
		powershellCommand := `Start-Process -FilePath "SchTasks" -ArgumentList ` + strings.Join(quotedArgs, ",") + ` -Verb RunAs -WindowStyle Hidden -Wait`
		command = exec.Command("powershell", "-NoProfile", "-Command", powershellCommand)
	}
	configureCoreCommand(command)
	return command.Run()
}

func writeAutoStartSetting(applicationPath string, enabled bool) error {
	if strings.TrimSpace(applicationPath) == "" {
		return errors.New("application path is empty")
	}

	var args []string
	if enabled {
		temporary, err := os.CreateTemp("", "zashdesktop-autostart-*.xml")
		if err != nil {
			return fmt.Errorf("create startup task file: %w", err)
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if _, err := temporary.Write(autoStartTaskXML(applicationPath)); err != nil {
			_ = temporary.Close()
			return fmt.Errorf("write startup task file: %w", err)
		}
		if err := temporary.Close(); err != nil {
			return fmt.Errorf("close startup task file: %w", err)
		}

		args = []string{
			"/Create",
			"/F",
			"/TN", autoStartTaskName,
			"/XML", temporaryPath,
		}
	} else {
		args = []string{"/Delete", "/TN", autoStartTaskName, "/F"}
	}

	if err := runAutoStartCommand(args); err != nil {
		var exitError *exec.ExitError
		if !enabled && errors.As(err, &exitError) {
			return nil
		}
		return fmt.Errorf("%s startup task: %w", map[bool]string{true: "enable", false: "disable"}[enabled], err)
	}
	return nil
}
