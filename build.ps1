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
$buildCompleted = $false

Push-Location $projectRoot
try {
    $env:GOCACHE = $goCachePath
    New-Item -ItemType Directory -Path $goCachePath -Force | Out-Null

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
    Invoke-NativeCommand "wails3" @("generate", "syso", "-arch", "amd64", "-icon", $iconPath, "-manifest", $manifestPath, "-info", $infoPath, "-out", $sysoPath)
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
    Remove-Item -LiteralPath $sysoPath -Force -ErrorAction SilentlyContinue
    if ($null -eq $previousGoCachePath) {
        Remove-Item Env:GOCACHE -ErrorAction SilentlyContinue
    } else {
        $env:GOCACHE = $previousGoCachePath
    }
    Pop-Location
}

Write-Host "Built $exePath"
