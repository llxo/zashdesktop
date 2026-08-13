package main

import (
	"embed"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	launch := parseLaunchConfig(os.Args[1:])
	controller := &App{launch: launch}

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

func (c LaunchConfig) windowOptions() application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name:             "main",
		Title:            "sing-box-gui",
		URL:              launchURL(c),
		Width:            1280,
		Height:           820,
		BackgroundColour: application.NewRGBA(18, 18, 18, 255),
	}
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
