package main

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type App struct {
	app      *application.App
	window   *application.WebviewWindow
	tray     *application.SystemTray
	launch   LaunchConfig
	quitting bool
	mu       sync.Mutex
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

	window := a.app.Window.NewWithOptions(a.launch.windowOptions())
	if !a.launch.NoTray {
		window.RegisterHook(events.Common.WindowClosing, a.releaseWindow)
	}
	a.window = window
	return window
}

func (a *App) releaseWindow(*application.WindowEvent) {
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
	tray.SetIcon(trayIcon)
	tray.SetTooltip("sing-box-gui")
	tray.SetMenu(menu)
}

func (a *App) quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.app.Quit()
}
