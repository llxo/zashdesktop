package main

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
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
	if a.window == nil {
		return
	}
	a.window.Show().Focus()
}

func (a *App) hideOnClose(event *application.WindowEvent) {
	a.mu.Lock()
	quitting := a.quitting
	a.mu.Unlock()
	if quitting {
		return
	}

	a.window.Hide()
	event.Cancel()
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
	tray.AttachWindow(a.window).WindowOffset(5)
}

func (a *App) quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.app.Quit()
}
