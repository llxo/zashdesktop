param(
    [string]$Version
)

$ErrorActionPreference = "Stop"

$projectRoot = $PSScriptRoot
$binDir = Join-Path $projectRoot "build\bin"
$artifactName = "zashdesktop"
$exePath = Join-Path $binDir "$artifactName.exe"
$frontendDir = Join-Path $projectRoot "frontend"
$viteCommand = Join-Path $frontendDir "node_modules\.bin\vite.cmd"
$bindingsDir = Join-Path $frontendDir "bindings"
$coreServiceBindingPath = Join-Path $bindingsDir "zashdesktop\coreservice.ts"
$modelsBindingPath = Join-Path $bindingsDir "zashdesktop\models.ts"
$goCachePath = Join-Path $projectRoot ".cache\go-build"
$sysoPath = Join-Path $projectRoot "wails_windows_amd64.syso"
$iconPath = Join-Path $projectRoot "build\windows\icon.ico"
$manifestPath = Join-Path $projectRoot "build\windows\wails.exe.manifest"
$infoPath = Join-Path $projectRoot "build\windows\info.json"
$versionedInfoPath = $null
$versionedManifestPath = $null

if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = "0.0.0"
}
$Version = $Version.Trim().TrimStart('v')
if ($Version -notmatch '^\d+\.\d+\.\d+$') {
    throw "Version must be a stable semantic version such as 1.0.0, got: $Version"
}
$windowsVersion = "$Version.0"

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
        [object[]]$Arguments
    )

    $flatArgs = @($Arguments | ForEach-Object { $_ })
    & $Command @flatArgs
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed with exit code $LASTEXITCODE"
    }
}

function Assert-Command {
    param(
        [Parameter(Mandatory)]
        [string]$Name
    )

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command is not available: $Name"
    }
}

Assert-Command "go"
Assert-Command "wails3"
Assert-Command "node"

$previousGoCachePath = $env:GOCACHE
$previousAppVersion = $env:APP_VERSION
$buildCompleted = $false
$resourceTempDir = $null

Push-Location $projectRoot
try {
    $env:GOCACHE = $goCachePath
    $env:APP_VERSION = $Version
    New-Item -ItemType Directory -Path $goCachePath -Force | Out-Null

    $resourceTempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("zashdesktop-build-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $resourceTempDir -Force | Out-Null

    $versionedInfoPath = Join-Path $resourceTempDir "info.json"
    $info = Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
    $info.fixed.file_version = $version
    $info.info.'0000'.ProductVersion = $version
    $info | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $versionedInfoPath -Encoding utf8

    $versionedManifestPath = Join-Path $resourceTempDir "wails.exe.manifest"
    $manifest = Get-Content -LiteralPath $manifestPath -Raw
    $manifest = $manifest -replace '(<assemblyIdentity type="win32" name="com\.singbox\.gui" version=")[^"]+(" processorArchitecture="\*"/>)', "`$1$windowsVersion`$2"
    Set-Content -LiteralPath $versionedManifestPath -Value $manifest -Encoding utf8

    if (-not (Test-Path -LiteralPath $binDir)) {
        New-Item -ItemType Directory -Path $binDir -Force | Out-Null
    }
    Remove-Item -LiteralPath $sysoPath -Force -ErrorAction SilentlyContinue

    if (-not (Test-Path -LiteralPath $viteCommand)) {
        Assert-Command "pnpm"
        Push-Location $frontendDir
        try {
            Write-Host "Installing frontend dependencies..."
            Invoke-NativeCommand "pnpm" @("install", "--frozen-lockfile", "--config.confirmModulesPurge=false")
        } finally {
            Pop-Location
        }
    }

    Write-Host "Generating Wails bindings..."
    Invoke-NativeCommand "wails3" @("generate", "bindings", "-ts", "-i", "-clean", "-d", "frontend/bindings")
    foreach ($bindingPath in @($coreServiceBindingPath, $modelsBindingPath)) {
        if (-not (Test-Path -LiteralPath $bindingPath)) {
            throw "Wails binding generation did not produce: $bindingPath"
        }
    }
    if (-not (Select-String -LiteralPath $coreServiceBindingPath -Pattern "SaveBehavior" -SimpleMatch -Quiet)) {
        throw "Generated CoreService binding is missing SaveBehavior: $coreServiceBindingPath"
    }

    Write-Host "Building frontend..."
    Push-Location $frontendDir
    try {
        Invoke-NativeCommand $viteCommand @("build", "--mode", "desktop", "--outDir", "dist", "--emptyOutDir")
    } finally {
        Pop-Location
    }
    $frontendIndexPath = Join-Path $frontendDir "dist\index.html"
    if (-not (Test-Path -LiteralPath $frontendIndexPath)) {
        throw "Frontend build did not produce: $frontendIndexPath"
    }

    Write-Host "Generating Windows resources..."
    Invoke-NativeCommand "wails3" @("generate", "syso", "-arch", "amd64", "-icon", $iconPath, "-manifest", $versionedManifestPath, "-info", $versionedInfoPath, "-out", $sysoPath)
    if (-not (Test-Path -LiteralPath $sysoPath)) {
        throw "Windows resource generation did not produce: $sysoPath"
    }

    Write-Host "Building Go application..."
    Invoke-NativeCommand "go" @("build", "-tags", "production", "-trimpath", "-buildvcs=false", "-ldflags=-w -s -H windowsgui", "-o", $exePath, ".")
    if (-not (Test-Path -LiteralPath $exePath)) {
        throw "Go build did not produce: $exePath"
    }
    $buildCompleted = $true
} finally {
    if (-not $buildCompleted) {
        Remove-Item -LiteralPath $exePath -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $resourceTempDir) {
        Remove-Item -LiteralPath $resourceTempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $sysoPath -Force -ErrorAction SilentlyContinue
    if ($null -eq $previousGoCachePath) {
        Remove-Item Env:GOCACHE -ErrorAction SilentlyContinue
    } else {
        $env:GOCACHE = $previousGoCachePath
    }
    if ($null -eq $previousAppVersion) {
        Remove-Item Env:APP_VERSION -ErrorAction SilentlyContinue
    } else {
        $env:APP_VERSION = $previousAppVersion
    }
    Pop-Location
}

Write-Host "Built $exePath (version $Version)"
