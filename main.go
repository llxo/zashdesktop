package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	launch := parseLaunchConfig(os.Args[1:])
	windowState, windowStatePath := loadWindowState()
	controller := &App{
		launch:          launch,
		windowState:     windowState,
		windowStatePath: windowStatePath,
	}

	app := application.New(application.Options{
		Name:        "sing-box-gui",
		Description: "A native sing-box dashboard using the Clash API",
		Icon:        appIcon,
		Assets: application.AssetOptions{
			Handler:        application.AssetFileServerFS(assets),
			DisableLogging: true,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: !launch.NoTray,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "sing-box-gui-single-instance",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				controller.showWindow()
			},
		},
	})
	controller.app = app

	if !launch.NoTray {
		controller.setupTray()
	}
	if !launch.StartHidden || launch.NoTray {
		controller.createWindow()
	}

	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "sing-box-gui:", err)
	}
}

const (
	defaultWindowWidth  = 1280
	defaultWindowHeight = 820
)

func (c LaunchConfig) windowOptions(state windowState) application.WebviewWindowOptions {
	options := application.WebviewWindowOptions{
		Name:             "main",
		Title:            "sing-box-gui",
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

	path := filepath.Join(configDir, "sing-box-gui", "window.json")
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
}

func parseLaunchConfig(args []string) LaunchConfig {
	flags := flag.NewFlagSet("sing-box-gui", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	config := LaunchConfig{}
	flags.StringVar(&config.APIURL, "api-url", "", "Clash API URL, for example http://127.0.0.1:9090")
	flags.StringVar(&config.APISecret, "api-secret", "", "Clash API secret")
	flags.StringVar(&config.APIType, "api-type", "clash", "API type: clash or singbox")
	flags.BoolVar(&config.StartHidden, "start-hidden", false, "Start in the system tray")
	flags.BoolVar(&config.NoTray, "no-tray", false, "Disable the system tray icon")
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
