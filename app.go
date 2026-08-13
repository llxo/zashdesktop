package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type App struct {
	app             *application.App
	window          *application.WebviewWindow
	tray            *application.SystemTray
	launch          LaunchConfig
	windowState     windowState
	windowStatePath string
	saveTimer       *time.Timer
	saveMu          sync.Mutex
	quitting        bool
	mu              sync.Mutex
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
		fmt.Printf("sing-box-gui: save window state: %v\n", err)
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
	menu := a.app.Menu.New()

	menu.Add("打开 sing-box-gui").OnClick(func(*application.Context) {
		a.showWindow()
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		a.quit()
	})

	a.tray = tray
	tray.SetIcon(appIcon)
	tray.SetTooltip("sing-box-gui")
	tray.SetMenu(menu)
	tray.OnClick(a.showWindow)
}

func (a *App) quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.app.Quit()
}
