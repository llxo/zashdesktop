$ErrorActionPreference = "Stop"

$projectRoot = $PSScriptRoot
$binDir = Join-Path $projectRoot "build\bin"
$artifactName = Get-Random -Minimum 100000 -Maximum 1000000
$exePath = Join-Path $binDir "$artifactName.exe"
$frontendDir = Join-Path $projectRoot "frontend"
$viteCommand = Join-Path $frontendDir "node_modules\.bin\vite.cmd"
$sysoPath = Join-Path $projectRoot "wails_windows_amd64.syso"
$iconPath = Join-Path $projectRoot "build\windows\icon.ico"
$manifestPath = Join-Path $projectRoot "build\windows\wails.exe.manifest"
$infoPath = Join-Path $projectRoot "build\windows\info.json"

foreach ($requiredPath in @($iconPath, $manifestPath, $infoPath, $frontendDir, (Join-Path $frontendDir "package.json"))) {
    if (-not (Test-Path -LiteralPath $requiredPath)) {
        throw "Required build input does not exist: $requiredPath"
    }
}

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory)]
        [string]$Command,
        [Parameter(ValueFromRemainingArguments)]
        [string[]]$Arguments
    )

    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed with exit code $LASTEXITCODE"
    }
}

Push-Location $projectRoot
try {
    if (-not (Test-Path -LiteralPath $binDir)) {
        New-Item -ItemType Directory -Path $binDir | Out-Null
    }
    Remove-Item -LiteralPath $sysoPath -Force -ErrorAction SilentlyContinue

    if (-not (Test-Path -LiteralPath $viteCommand)) {
        Push-Location $frontendDir
        try {
            Invoke-NativeCommand "pnpm" @("install", "--frozen-lockfile", "--config.confirmModulesPurge=false")
        } finally {
            Pop-Location
        }
    }
    Push-Location $frontendDir
    try {
        Invoke-NativeCommand $viteCommand @("build", "--mode", "desktop", "--outDir", "dist", "--emptyOutDir")
    } finally {
        Pop-Location
    }

    Invoke-NativeCommand "wails3" @("generate", "syso", "-arch", "amd64", "-icon", $iconPath, "-manifest", $manifestPath, "-info", $infoPath, "-out", $sysoPath)
    Invoke-NativeCommand "go" @("build", "-a", "-tags", "production", "-trimpath", "-buildvcs=false", "-ldflags=-w -s -H windowsgui", "-o", $exePath, ".")
} finally {
    Remove-Item -LiteralPath $sysoPath -Force -ErrorAction SilentlyContinue
    Pop-Location
}

Write-Host "Built $exePath"
