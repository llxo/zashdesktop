package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
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
		debugLogf("system", "write RunAsAdmin setting failed: empty application path")
		return errors.New("application path is empty")
	}

	if current, err := readRunAsAdminSetting(applicationPath); err == nil && current == enabled {
		return nil
	}

	if enabled {
		key, _, err := registry.CreateKey(registry.CURRENT_USER, behaviorLayersKey, registry.SET_VALUE)
		if err != nil {
			debugLogf("system", "create registry key %q failed: %v", behaviorLayersKey, err)
			return fmt.Errorf("create administrator compatibility settings: %w", err)
		}
		defer key.Close()
		if err := key.SetStringValue(applicationPath, "RunAsAdmin"); err != nil {
			debugLogf("system", "set RunAsAdmin registry value for %q failed: %v", applicationPath, err)
			return fmt.Errorf("enable administrator mode: %w", err)
		}
		debugLogf("system", "enabled RunAsAdmin registry setting for %q", applicationPath)
		return nil
	}

	key, err := registry.OpenKey(registry.CURRENT_USER, behaviorLayersKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		debugLogf("system", "open registry key %q failed: %v", behaviorLayersKey, err)
		return fmt.Errorf("open administrator compatibility settings: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(applicationPath); err != nil && !errors.Is(err, registry.ErrNotExist) {
		debugLogf("system", "delete RunAsAdmin registry value for %q failed: %v", applicationPath, err)
		return fmt.Errorf("disable administrator mode: %w", err)
	}
	debugLogf("system", "disabled RunAsAdmin registry setting for %q", applicationPath)
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

func encodeUTF16LEWithBOM(s string) []byte {
	runes := utf16.Encode([]rune(s))
	bytes := make([]byte, 2+len(runes)*2)
	bytes[0] = 0xFF
	bytes[1] = 0xFE
	for i, r := range runes {
		bytes[2+i*2] = byte(r)
		bytes[3+i*2] = byte(r >> 8)
	}
	return bytes
}

func autoStartTaskXML(applicationPath string) []byte {
	escapedTaskName := xmlEscape(autoStartTaskName)
	escapedAppPath := xmlEscape(applicationPath)
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
</Task>`, escapedTaskName, autoStartDelay, escapedAppPath)
	return encodeUTF16LEWithBOM(configuration)
}

func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
	command := exec.Command("SchTasks", args...)
	configureCoreCommand(command)
	return command.Run()
}

func writeAutoStartSetting(applicationPath string, enabled bool) error {
	if strings.TrimSpace(applicationPath) == "" {
		debugLogf("system", "write auto start setting failed: empty application path")
		return errors.New("application path is empty")
	}

	current, err := readAutoStartSetting()
	if err == nil && current == enabled {
		return nil
	}
	if !enabled && !current {
		return nil
	}

	privileged, err := isPrivileged()
	if err != nil || !privileged {
		debugLogf("system", "write auto start setting failed: administrator privilege required (err=%v)", err)
		return errors.New("需要管理员权限才能配置自启动任务")
	}

	var args []string
	if enabled {
		temporary, err := os.CreateTemp("", "zashdesktop-autostart-*.xml")
		if err != nil {
			debugLogf("system", "create autostart temp xml failed: %v", err)
			return fmt.Errorf("create startup task file: %w", err)
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if _, err := temporary.Write(autoStartTaskXML(applicationPath)); err != nil {
			_ = temporary.Close()
			debugLogf("system", "write autostart temp xml failed: %v", err)
			return fmt.Errorf("write startup task file: %w", err)
		}
		if err := temporary.Close(); err != nil {
			debugLogf("system", "close autostart temp xml failed: %v", err)
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
		debugLogf("system", "execute schtasks command %v failed: %v", args, err)
		return fmt.Errorf("%s startup task: %w", map[bool]string{true: "enable", false: "disable"}[enabled], err)
	}
	debugLogf("system", "write auto start setting success: enabled=%t", enabled)
	return nil
}

func ensureProgramDataShortcut(applicationPath string) error {
	if strings.TrimSpace(applicationPath) == "" {
		return errors.New("application path is empty")
	}

	privileged, err := isPrivileged()
	if err != nil || !privileged {
		return nil
	}

	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	startMenuDir := filepath.Join(programData, `Microsoft\Windows\Start Menu\Programs`)
	if err := os.MkdirAll(startMenuDir, 0o755); err != nil {
		debugLogf("system", "create start menu directory %q failed: %v", startMenuDir, err)
		return fmt.Errorf("create start menu directory: %w", err)
	}

	shortcutBase := strings.TrimSuffix(filepath.Base(applicationPath), filepath.Ext(applicationPath))
	if shortcutBase == "" {
		shortcutBase = "zashdesktop"
	}
	shortcutPath := filepath.Join(startMenuDir, shortcutBase+".lnk")

	// 若快捷方式已存在则跳过，避免重复拉起 PowerShell 进程
	if _, err := os.Stat(shortcutPath); err == nil {
		return nil
	}

	workDir := filepath.Dir(applicationPath)

	psScript := fmt.Sprintf(
		`$wsh = New-Object -ComObject WScript.Shell; $s = $wsh.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.WorkingDirectory = '%s'; $s.IconLocation = '%s,0'; $s.Description = '%s'; $s.Save()`,
		strings.ReplaceAll(shortcutPath, `'`, `''`),
		strings.ReplaceAll(applicationPath, `'`, `''`),
		strings.ReplaceAll(workDir, `'`, `''`),
		strings.ReplaceAll(applicationPath, `'`, `''`),
		strings.ReplaceAll(shortcutBase, `'`, `''`),
	)

	command := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	configureCoreCommand(command)
	if err := command.Run(); err != nil {
		debugLogf("system", "create start menu shortcut via PowerShell failed: %v", err)
		return err
	}
	debugLogf("system", "created start menu shortcut at %q", shortcutPath)
	return nil
}

// -----------------------------------------------------------------------------
// Windows System Proxy Settings
// -----------------------------------------------------------------------------

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func systemProxy(request *http.Request) (*url.URL, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return http.ProxyFromEnvironment(request)
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return http.ProxyFromEnvironment(request)
	}
	proxyServer, _, err := key.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(proxyServer) == "" {
		return http.ProxyFromEnvironment(request)
	}
	override, _, _ := key.GetStringValue("ProxyOverride")
	if proxyBypassed(request.URL, override) {
		return nil, nil
	}

	address := proxyAddressForScheme(proxyServer, request.URL.Scheme)
	if address == "" {
		return http.ProxyFromEnvironment(request)
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	proxyURL, err := url.Parse(address)
	if err != nil || proxyURL.Host == "" {
		return http.ProxyFromEnvironment(request)
	}
	return proxyURL, nil
}

func proxyAddressForScheme(proxyServer, scheme string) string {
	defaultAddress := ""
	for _, item := range strings.Split(proxyServer, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			if defaultAddress == "" {
				defaultAddress = item
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[0]), scheme) {
			return strings.TrimSpace(parts[1])
		}
	}
	return defaultAddress
}

func proxyBypassed(target *url.URL, override string) bool {
	host := strings.ToLower(target.Hostname())
	for _, item := range strings.Split(override, ";") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if item == "<local>" && !strings.Contains(host, ".") {
			return true
		}
		item = strings.TrimPrefix(item, "*")
		if strings.HasPrefix(host, item) || strings.HasSuffix(host, item) {
			return true
		}
	}
	return false
}


