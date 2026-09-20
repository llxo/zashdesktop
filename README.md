# zashdesktop

[English](./README.md) | [简体中文](./README_zh.md)

A lightweight desktop client for raw sing-box and mihomo cores on Windows. Unlike traditional heavy GUI wrappers, it focuses on providing a clean desktop shell for bare cores: frontend deeply customized and ported from [zashboard](https://github.com/Zephyruso/zashboard), combined with Wails v3 and the system WebView2 runtime to deliver native desktop capabilities. It integrates dual-core management, subscription maintenance, and system tray control, with ultra-low background memory usage and instant wake-up.

## Features

- **Dual-Core Management**: Native support for both sing-box and mihomo bare cores, with core binaries and configurations completely isolated and untouched.
- **Online Core Updates**: Built-in accelerated mirror sources with one-click checking and updating for stable and beta channels.
- **Config Import & Switching**: One-click subscription downloading or local file importing, with instant multi-profile switching via a dropdown menu.
- **Low Memory Background Suspension**: Automatically suspends the rendering process and trims the physical working set when the window is closed (idle memory ~20–40 MB), with instantaneous response on call.
- **System Tray Controls**: Quick tray menu actions to open dashboard, clear cache, start/stop/restart cores, and switch proxy nodes.
- **Auto-Start & Process Companion**: Supports auto-start on boot (via Windows Task Scheduler), core lifecycle synchronization with the app, and running as administrator.
- **Client One-Click Updates**: Check and update the zashdesktop client directly from the settings page.
- **Polished Dashboard Experience**: Frontend based on the excellent [zashboard](https://github.com/Zephyruso/zashboard) with custom adaptations, offering proxy group management, latency testing, connection tracking, routing rules, and multi-language support.

## Resource Usage

| State | Memory Usage | Notes |
| :--- | :---: | :--- |
| **Window Open** | ~100–300 MB | Rendered via system WebView2 runtime |
| **Window Closed (Tray Background)** | ~20–40 MB | Suspends rendering process and releases physical working set |

> [!NOTE]
> The figures above apply to the desktop client shell only, excluding the proxy core's own memory.

## Quick Start

On first launch, navigate to the **"Core"** tab in the bottom bar to configure and start your core:

1. **Prepare the Core**: Select the desired tab (`sing-box` or `mihomo`), choose the channel (Release / Pre-release) and download mirror under "Download Core", then click **"Update"** to automatically download and install.
2. **Import Configuration**: Paste your subscription link under "Configuration" and click **"Download"**, or click **"Import"** to choose a local file. Select the active configuration in the "Active Profile" dropdown.
3. **Start the Core**: Click **"Start"** to launch the core. Custom command-line arguments are supported (default: `run -c "config.json" -D .`).
4. **Automation Settings**: Switch to the **"Settings"** tab to configure as needed:
   - **Run as Administrator**: Enable if you need TUN virtual network adapter or privileged network features.
   - **Start on Boot (Admin Required)**: Silently launch the client in the background on system boot.
   - **Companion Lifecycle**: Optionally enable "Start sing-box / mihomo on app launch" and "Stop core on app exit".
   - **Client Updates**: Check for updates and upgrade the zashdesktop client with one click.

> [!TIP]
> The default proxy controller address is `http://127.0.0.1:9090`. To change the port or secret, configure them in the bottom bar under **"Settings"** -> **"Backend"**.

## Directory Structure & Manual Core Setup

The application directory structure is outlined below. It supports direct manual copying and replacement of binaries and configuration files:

```text
zashdesktop/
├─ mihomo/
│  ├─ mihomo.exe       # mihomo core executable
│  └─ config.yaml      # configuration file
├─ sing-box/
│  ├─ sing-box.exe     # sing-box core executable
│  └─ config.json      # configuration file
├─ profiles.json       # configuration and app profile state
└─ zashdesktop.exe     # main client executable
```

## Troubleshooting

- **Core Failed to Start**: Click "View Logs" in the notification prompt to inspect detailed errors; verify that your configuration syntax is valid.
- **TUN Mode Errors**: Turn on "Run as Administrator" in "Core" -> "Settings".
- **Backend Diagnosis**: Enable "Backend Debug Log" in "Core" -> "Settings", and check `debug.log` in the application directory.

## Uninstallation & Cache Cleanup

If you want to completely remove the application along with saved window states and WebView2 cache data, delete the following directory:

- Path: `%APPDATA%\zashdesktop` (i.e. `C:\Users\<Username>\AppData\Roaming\zashdesktop`)

## Building from Source

Prerequisites: Go 1.22+, Node.js (pnpm), Wails v3 CLI.

Run the one-click build script in Windows PowerShell:

```powershell
.\build.ps1
```

The compiled binary will be located at `build/bin/zashdesktop.exe`.
