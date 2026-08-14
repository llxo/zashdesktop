# zashdesktop

Windows x64 desktop host. It uses Wails v3 and the system WebView2 runtime, so the frontend stays embedded without opening an external browser.

## Build

The frontend is the zashboard application under `frontend/`. The desktop build
uses system fonts and writes its static assets to `frontend/dist`.

Build the frontend assets and Windows executable:

```powershell
.\build.ps1
```

The build script clears the Go build cache and old binaries, embeds
`build/windows/icon.ico` as the application and tray icon, and embeds the same
icon into the Windows executable resources.

## Command line

```text
zashdesktop.exe --api-url http://127.0.0.1:9090 --api-secret secret
zashdesktop.exe --start-hidden
zashdesktop.exe --no-tray
```

With the tray enabled, closing the window destroys the WebView2 instance to reduce background memory
usage. Use the tray menu to create the window again or exit the process.
