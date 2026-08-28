package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

var appVersion = "0.0.0"

func main() {
	cleanOldUpdateFiles()
	launch := parseLaunchConfig(os.Args[1:])
	windowState, windowStatePath := loadWindowState()
	coreService, err := NewCoreService()
	if err != nil {
		log.Fatalf("failed to initialize core service: %v", err)
	}
	controller := &App{
		coreService:     coreService,
		launch:          launch,
		windowState:     windowState,
		windowStatePath: windowStatePath,
	}

	configDir, _ := os.UserConfigDir()
	userDataPath := ""
	if configDir != "" {
		userDataPath = filepath.Join(configDir, "zashdesktop")
	}

	app := application.New(application.Options{
		Name:        "zashdesktop",
		Description: "A native sing-box dashboard using the Clash API",
		Icon:        appIcon,
		Assets: application.AssetOptions{
			Handler:        application.AssetFileServerFS(assets),
			DisableLogging: true,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: !launch.NoTray,
			WebviewUserDataPath:           userDataPath,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: singleInstanceID(launch),
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				controller.showWindow()
			},
		},
		Services: []application.Service{
			application.NewService(controller.coreService),
		},
	})
	controller.app = app
	controller.coreService.app = app

	if !launch.NoTray {
		controller.setupTray()
	}
	if !launch.StartHidden || launch.NoTray {
		controller.createWindow()
	}

	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "zashdesktop:", err)
	}
}

const (
	defaultWindowWidth  = 1280
	defaultWindowHeight = 820
)

func (c LaunchConfig) windowOptions(state windowState) application.WebviewWindowOptions {
	options := application.WebviewWindowOptions{
		Name:             "main",
		Title:            "zashdesktop",
		URL:              launchURL(c),
		Width:            defaultWindowWidth,
		Height:           defaultWindowHeight,
		BackgroundColour: application.NewRGBA(18, 18, 18, 255),
	}
	if state.valid() {
		options.Width = state.Width
		options.Height = state.Height
		options.InitialPosition = application.WindowXY
		options.X = state.X
		options.Y = state.Y
	}
	return options
}

type windowState struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (s windowState) valid() bool {
	return s.Width >= 320 && s.Height >= 240 && s.Width <= 10000 && s.Height <= 10000
}

func loadWindowState() (windowState, string) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return windowState{}, ""
	}

	path := filepath.Join(configDir, "zashdesktop", "window.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return windowState{}, path
	}

	var state windowState
	if err := json.Unmarshal(data, &state); err != nil || !state.valid() {
		return windowState{}, path
	}

	return state, path
}

func saveWindowState(path string, state windowState) error {
	if path == "" || !state.valid() {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".window-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	return os.Rename(temporaryPath, path)
}

type LaunchConfig struct {
	APIURL      string
	APISecret   string
	APIType     string
	StartHidden bool
	NoTray      bool
	DevMode     bool
}

func singleInstanceID(config LaunchConfig) string {
	if config.DevMode {
		return "zashdesktop-dev-single-instance"
	}
	return "zashdesktop-single-instance"
}

func parseLaunchConfig(args []string) LaunchConfig {
	flags := flag.NewFlagSet("zashdesktop", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	config := LaunchConfig{}
	flags.StringVar(&config.APIURL, "api-url", "", "Clash API URL, for example http://127.0.0.1:9090")
	flags.StringVar(&config.APISecret, "api-secret", "", "Clash API secret")
	flags.StringVar(&config.APIType, "api-type", "clash", "API type: clash or sing-box")
	flags.BoolVar(&config.StartHidden, "start-hidden", false, "Start in the system tray")
	flags.BoolVar(&config.NoTray, "no-tray", false, "Disable the system tray icon")
	flags.BoolVar(&config.DevMode, "dev", false, "Use the development instance")
	_ = flags.Parse(args)

	return config
}

func launchURL(config LaunchConfig) string {
	if strings.TrimSpace(config.APIURL) == "" {
		return "/"
	}

	apiURL, err := url.Parse(config.APIURL)
	if err != nil || apiURL.Hostname() == "" {
		return "/"
	}

	apiType := strings.ToLower(strings.TrimSpace(config.APIType))
	if apiType == "sing-box" {
		apiType = "singbox"
	}
	if apiType != "singbox" {
		apiType = "clash"
	}

	port := apiURL.Port()
	if port == "" {
		if apiURL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	query := url.Values{
		"hostname":      []string{apiURL.Hostname()},
		"port":          []string{port},
		"secondaryPath": []string{secondaryPath(apiURL.Path)},
		"secret":        []string{config.APISecret},
		"type":          []string{apiType},
	}
	if apiURL.Scheme == "https" {
		query.Set("https", "1")
	} else {
		query.Set("http", "1")
	}

	return "/#/setup?" + query.Encode()
}

func secondaryPath(path string) string {
	if path == "" || path == "/" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func cleanOldUpdateFiles() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return
	}
	dir := filepath.Dir(executable)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".old") || (strings.HasPrefix(name, ".") && strings.Contains(name, "-update-") && strings.HasSuffix(name, ".tmp")) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// -----------------------------------------------------------------------------
// App Controller & Window Management
// -----------------------------------------------------------------------------

type App struct {
	app                  *application.App
	coreService          *CoreService
	window               *application.WebviewWindow
	tray                 *application.SystemTray
	launch               LaunchConfig
	windowState          windowState
	windowStatePath      string
	saveTimer            *time.Timer
	saveMu               sync.Mutex
	trayProxyCancel      context.CancelFunc
	trayProxyFingerprint string
	trayProxyRefreshMu   sync.Mutex
	clearCacheMu         sync.Mutex
	quitting             bool
	mu                   sync.Mutex
}

func (a *App) showWindow() {
	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		return
	}
	window := a.window
	if window == nil {
		window = a.createWindowLocked()
	}
	a.mu.Unlock()

	window.Show().Focus()
}

func (a *App) createWindow() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.window == nil && !a.quitting {
		a.createWindowLocked()
	}
}

func (a *App) createWindowLocked() *application.WebviewWindow {
	if a.window != nil {
		return a.window
	}

	window := a.app.Window.NewWithOptions(a.launch.windowOptions(a.windowState))
	window.OnWindowEvent(events.Common.WindowDidMove, a.scheduleWindowStateSave)
	window.OnWindowEvent(events.Common.WindowDidResize, a.scheduleWindowStateSave)
	window.RegisterHook(events.Common.WindowClosing, a.releaseWindow)
	a.window = window
	return window
}

func (a *App) scheduleWindowStateSave(*application.WindowEvent) {
	a.mu.Lock()
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	a.saveTimer = time.AfterFunc(250*time.Millisecond, func() {
		a.saveWindowState()
	})
	a.mu.Unlock()
}

func (a *App) saveWindowStateFromWindow(*application.WindowEvent) {
	a.saveWindowState()
}

func (a *App) saveWindowState() {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()

	a.mu.Lock()
	window := a.window
	a.mu.Unlock()
	if window == nil || window.IsMaximised() || window.IsMinimised() || window.IsFullscreen() {
		return
	}

	x, y := window.Position()
	width, height := window.Size()
	state := windowState{X: x, Y: y, Width: width, Height: height}
	if err := saveWindowState(a.windowStatePath, state); err != nil {
		fmt.Printf("zashdesktop: save window state: %v\n", err)
		return
	}

	a.mu.Lock()
	a.windowState = state
	a.mu.Unlock()
}

func (a *App) releaseWindow(*application.WindowEvent) {
	a.mu.Lock()
	if a.saveTimer != nil {
		a.saveTimer.Stop()
		a.saveTimer = nil
	}
	a.mu.Unlock()
	a.saveWindowState()
	// Do not cancel the close event. Wails will destroy the window and release
	// WebView2, including the page's workers, sockets, and graphics resources.
	a.mu.Lock()
	a.window = nil
	a.mu.Unlock()
}

func (a *App) setupTray() {
	tray := a.app.SystemTray.New()

	a.tray = tray
	tray.SetIcon(appIcon)
	tray.SetTooltip("zashdesktop")

	var initialConfig CoreConfig
	if a.coreService != nil {
		if config, err := a.coreService.GetConfig(); err == nil {
			initialConfig = config
		}
		a.coreService.setOnStateChange(func() {
			a.refreshTrayProxyGroups(context.Background())
		})
	}

	a.setTrayMenu(initialConfig, nil)
	tray.OnClick(a.showWindow)
	a.startTrayProxyRefresh()
}

func (a *App) quit() {
	a.mu.Lock()
	a.quitting = true
	cancelTrayProxy := a.trayProxyCancel
	a.mu.Unlock()
	if cancelTrayProxy != nil {
		cancelTrayProxy()
	}
	a.app.Quit()
}

func (a *App) clearFrontendCache() {
	if !a.clearCacheMu.TryLock() {
		return
	}
	defer a.clearCacheMu.Unlock()

	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		return
	}
	window := a.window
	wasOpen := window != nil
	a.mu.Unlock()

	if wasOpen {
		window.Close()
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			a.mu.Lock()
			currentWindow := a.window
			a.mu.Unlock()
			if currentWindow == nil {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}

	for _, dir := range getWebviewCacheDirs() {
		_ = removeDirectoryWithRetry(dir, 5, 100*time.Millisecond)
	}

	if wasOpen {
		a.showWindow()
	}
}

func removeDirectoryWithRetry(dir string, maxAttempts int, delay time.Duration) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		lastErr = os.RemoveAll(dir)
		if lastErr == nil {
			return nil
		}
		time.Sleep(delay)
	}
	return lastErr
}

func getWebviewCacheDirs() []string {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		return nil
	}
	return []string{filepath.Join(configDir, "zashdesktop", "EBWebView")}
}


