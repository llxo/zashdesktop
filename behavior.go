package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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
	behaviorAdvapi32                              = windows.NewLazySystemDLL("advapi32.dll")
	behaviorCheckTokenMembership                  = behaviorAdvapi32.NewProc("CheckTokenMembership")
	behaviorWinHTTP                               = windows.NewLazySystemDLL("winhttp.dll")
	behaviorWinHttpGetIEProxyConfigForCurrentUser = behaviorWinHTTP.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	behaviorKernel32                              = windows.NewLazySystemDLL("kernel32.dll")
	behaviorGlobalFree                            = behaviorKernel32.NewProc("GlobalFree")
	behaviorSetProcessWorkingSetSize              = behaviorKernel32.NewProc("SetProcessWorkingSetSize")
	behaviorPsapi                                 = windows.NewLazySystemDLL("psapi.dll")
	behaviorEmptyWorkingSet                       = behaviorPsapi.NewProc("EmptyWorkingSet")
	behaviorNtdll                                 = windows.NewLazySystemDLL("ntdll.dll")
	behaviorNtSuspendProcess                      = behaviorNtdll.NewProc("NtSuspendProcess")
	behaviorNtResumeProcess                       = behaviorNtdll.NewProc("NtResumeProcess")
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
	if !enabled && (err != nil || !current) {
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
// Windows System Proxy Settings (WinHTTP Native API)
// -----------------------------------------------------------------------------

type winHTTPCurrentUserIEProxyConfig struct {
	fAutoDetect       int32
	lpszAutoConfigURL *uint16
	lpszProxy         *uint16
	lpszProxyBypass   *uint16
}

const proxySettingsCacheTTL = 60 * time.Second

type cachedProxySettings struct {
	enabled   bool
	server    string
	override  string
	fetchedAt time.Time
}

var proxySettingsCache struct {
	sync.Mutex
	settings cachedProxySettings
}

func readCachedProxySettings() (enabled bool, server, override string) {
	proxySettingsCache.Lock()
	defer proxySettingsCache.Unlock()
	if !proxySettingsCache.settings.fetchedAt.IsZero() && time.Since(proxySettingsCache.settings.fetchedAt) < proxySettingsCacheTTL {
		s := proxySettingsCache.settings
		return s.enabled, s.server, s.override
	}

	var config winHTTPCurrentUserIEProxyConfig
	r1, _, err := behaviorWinHttpGetIEProxyConfigForCurrentUser.Call(uintptr(unsafe.Pointer(&config)))
	if r1 == 0 {
		debugLogf("system", "WinHttpGetIEProxyConfigForCurrentUser query failed: %v", err)
		proxySettingsCache.settings = cachedProxySettings{fetchedAt: time.Now()}
		return false, "", ""
	}
	defer func() {
		if config.lpszAutoConfigURL != nil {
			_, _, _ = behaviorGlobalFree.Call(uintptr(unsafe.Pointer(config.lpszAutoConfigURL)))
		}
		if config.lpszProxy != nil {
			_, _, _ = behaviorGlobalFree.Call(uintptr(unsafe.Pointer(config.lpszProxy)))
		}
		if config.lpszProxyBypass != nil {
			_, _, _ = behaviorGlobalFree.Call(uintptr(unsafe.Pointer(config.lpszProxyBypass)))
		}
	}()

	var serverStr, overrideStr string
	if config.lpszProxy != nil {
		serverStr = strings.TrimSpace(windows.UTF16PtrToString(config.lpszProxy))
	}
	if config.lpszProxyBypass != nil {
		overrideStr = strings.TrimSpace(windows.UTF16PtrToString(config.lpszProxyBypass))
	}

	settings := cachedProxySettings{
		enabled:   serverStr != "",
		server:    serverStr,
		override:  overrideStr,
		fetchedAt: time.Now(),
	}
	proxySettingsCache.settings = settings
	return settings.enabled, settings.server, settings.override
}

func systemProxy(request *http.Request) (*url.URL, error) {
	enabled, server, override := readCachedProxySettings()
	if !enabled || server == "" {
		return http.ProxyFromEnvironment(request)
	}
	if proxyBypassed(request.URL, override) {
		return nil, nil
	}

	address := proxyAddressForScheme(server, request.URL.Scheme)
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

var (
	fontExtRegex   = regexp.MustCompile(`\s*\([^)]*\)$`)
	fontStyleRegex = regexp.MustCompile(`(?i)\s+(Bold(\s+Italic)?|Italic|Light(\s+Italic)?|Regular|Medium|Semibold|SemiBold|Semi-Bold|ExtraBold|Extra-Bold|Black|Heavy|Thin|ExtraLight|Extra-Light|Ultralight|SemiLight|Semi-Light|Normal|Oblique)$`)
)

func (s *CoreService) GetSystemFonts() []string {
	fontSet := make(map[string]struct{})

	readFontsFromKey := func(root registry.Key, path string) {
		k, err := registry.OpenKey(root, path, registry.READ)
		if err != nil {
			return
		}
		defer k.Close()

		names, err := k.ReadValueNames(-1)
		if err != nil {
			return
		}

		for _, rawName := range names {
			clean := fontExtRegex.ReplaceAllString(rawName, "")
			parts := strings.Split(clean, "&")
			for _, part := range parts {
				name := strings.TrimSpace(part)
				if name == "" {
					continue
				}
				family := fontStyleRegex.ReplaceAllString(name, "")
				family = strings.TrimSpace(family)
				if family != "" {
					fontSet[family] = struct{}{}
				} else {
					fontSet[name] = struct{}{}
				}
			}
		}
	}

	readFontsFromKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`)
	readFontsFromKey(registry.CURRENT_USER, `Software\Microsoft\Windows NT\CurrentVersion\Fonts`)

	list := make([]string, 0, len(fontSet))
	for f := range fontSet {
		list = append(list, f)
	}

	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i]) < strings.ToLower(list[j])
	})

	debugLogf("system", "enumerated %d system font families", len(list))
	return list
}

// -----------------------------------------------------------------------------
// WebView2 Process Suspend / Resume & Working Set Optimization
// -----------------------------------------------------------------------------

func trimProcessWorkingSet(h windows.Handle) {
	if behaviorEmptyWorkingSet.Find() == nil {
		behaviorEmptyWorkingSet.Call(uintptr(h))
	} else if behaviorSetProcessWorkingSetSize.Find() == nil {
		behaviorSetProcessWorkingSetSize.Call(uintptr(h), ^uintptr(0), ^uintptr(0))
	}
}

func findWebView2ProcessPIDs() []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil
	}

	type proc struct {
		pid, parent uint32
		isWebView   bool
	}
	var procs []proc
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		procs = append(procs, proc{
			pid:       entry.ProcessID,
			parent:    entry.ParentProcessID,
			isWebView: strings.EqualFold(name, "msedgewebview2.exe"),
		})
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}

	isDescendant := map[uint32]bool{uint32(os.Getpid()): true}
	for changed := true; changed; {
		changed = false
		for _, p := range procs {
			if !isDescendant[p.pid] && isDescendant[p.parent] {
				isDescendant[p.pid] = true
				changed = true
			}
		}
	}

	var webviewPIDs []uint32
	for _, p := range procs {
		if p.isWebView && isDescendant[p.pid] {
			webviewPIDs = append(webviewPIDs, p.pid)
		}
	}
	return webviewPIDs
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

func getProcessCommandLine(h windows.Handle) string {
	var returnLength uint32
	_ = windows.NtQueryInformationProcess(h, 60, nil, 0, &returnLength)
	if returnLength == 0 {
		returnLength = 2048
	}
	buf := make([]byte, returnLength+64)
	if err := windows.NtQueryInformationProcess(h, 60, unsafe.Pointer(&buf[0]), uint32(len(buf)), &returnLength); err != nil {
		return ""
	}
	if len(buf) < int(unsafe.Sizeof(unicodeString{})) {
		return ""
	}
	us := (*unicodeString)(unsafe.Pointer(&buf[0]))
	if us.Buffer == nil || us.Length == 0 {
		return ""
	}

	bufStart := uintptr(unsafe.Pointer(&buf[0]))
	bufEnd := bufStart + uintptr(len(buf))
	ptrVal := uintptr(unsafe.Pointer(us.Buffer))
	if ptrVal < bufStart || ptrVal+uintptr(us.Length) > bufEnd || ptrVal%2 != 0 {
		return ""
	}

	charCount := int(us.Length / 2)
	slice := unsafe.Slice(us.Buffer, charCount)
	return string(utf16.Decode(slice))
}

func suspendWebView2Processes() []uint32 {
	if behaviorNtSuspendProcess.Find() != nil {
		return nil
	}
	pids := findWebView2ProcessPIDs()
	const access = 0x0800 | windows.PROCESS_QUERY_INFORMATION | windows.PROCESS_SET_QUOTA | windows.PROCESS_QUERY_LIMITED_INFORMATION
	var suspended []uint32
	for _, pid := range pids {
		if h, err := windows.OpenProcess(access, false, pid); err == nil {
			cmdLine := getProcessCommandLine(h)
			// 差异化挂起核心分流：
			// 仅挂起沙盒渲染进程（--type=renderer），坚决放行主 Browser 进程、GPU 进程与 Utility 进程。
			// 确保主 Browser 进程的 Windows 消息泵与 UIA 探针畅通无阻，从根本上杜绝任务管理器 IPC 超时死锁与全局降级。
			if strings.Contains(cmdLine, "--type=renderer") {
				if r, _, callErr := behaviorNtSuspendProcess.Call(uintptr(h)); r == 0 {
					suspended = append(suspended, pid)
					debugLogf("window", "suspended webview2 renderer process (pid=%d)", pid)
				} else {
					debugLogf("window", "failed to suspend webview2 renderer process (pid=%d, status=0x%x): %v", pid, r, callErr)
				}
			}
			// 对所有 WebView2 相关子进程修剪物理工作集，释放 RAM
			trimProcessWorkingSet(h)
			windows.CloseHandle(h)
		}
	}
	if h, err := windows.GetCurrentProcess(); err == nil {
		trimProcessWorkingSet(h)
	}
	return suspended
}

func resumeWebView2Processes(pids []uint32) {
	if len(pids) == 0 || behaviorNtResumeProcess.Find() != nil {
		return
	}
	for _, pid := range pids {
		h, err := windows.OpenProcess(0x0800, false, pid)
		if err != nil {
			debugLogf("window", "failed to open webview2 renderer process for resume (pid=%d): %v", pid, err)
			continue
		}
		r, _, callErr := behaviorNtResumeProcess.Call(uintptr(h))
		windows.CloseHandle(h)
		if r != 0 {
			debugLogf("window", "failed to resume webview2 renderer process (pid=%d, status=0x%x): %v", pid, r, callErr)
		} else {
			debugLogf("window", "resumed webview2 renderer process (pid=%d)", pid)
		}
	}
}


