# sing-box-gui

Windows x64 desktop host. It uses Wails v3 and the system WebView2 runtime, so the frontend stays embedded without opening an external browser.

## Build

Prepare the frontend assets in `frontend/dist` before building:

```powershell
pnpm exec vite build --mode desktop --outDir frontend/dist --emptyOutDir
```

Build the Windows executable:

```powershell
cd desktop
go mod tidy
go build -o build/bin/sing-box-gui.exe .
```

## Command line

```text
sing-box-gui.exe --api-url http://127.0.0.1:9090 --api-secret secret
sing-box-gui.exe --start-hidden
sing-box-gui.exe --no-tray
```

With the tray enabled, closing the window destroys the WebView2 instance to reduce background memory
usage. Use the tray menu to create the window again or exit the process.
