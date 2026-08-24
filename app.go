package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

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

func (a *App) quitWithoutStoppingCore() {
	if a.coreService != nil {
		a.coreService.keepCoreRunningOnShutdown()
	}
	a.quit()
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

