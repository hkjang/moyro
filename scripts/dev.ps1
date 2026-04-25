[CmdletBinding()]
param(
    [switch]$SkipInfra,
    [switch]$NoServer,
    [switch]$NoWeb,
    [int]$ServerPort = 8065,
    [int]$WebPort = 5173
)

$ErrorActionPreference = "Stop"

$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
$ServerDir = Join-Path $Root "server"
$WebappDir = Join-Path $Root "webapp"
$PluginDir = Join-Path $Root "plugins"
$ComposeFile = Join-Path $Root "deploy\docker\compose.dev.yaml"

function Resolve-NpmCmd {
    $cmd = Get-Command npm.cmd -ErrorAction SilentlyContinue
    if ($cmd) {
        return $cmd.Source
    }

    $cmd = Get-Command npm -ErrorAction SilentlyContinue
    if ($cmd) {
        return $cmd.Source
    }

    throw "npm was not found on PATH."
}

function Invoke-Native {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw ("Command failed with exit code {0}: {1} {2}" -f $LASTEXITCODE, $FilePath, ($Arguments -join ' '))
    }
}

function Quote-PSString {
    param([string]$Value)
    return "'" + ($Value -replace "'", "''") + "'"
}

function Test-ContainsPath {
    param(
        [AllowNull()][string]$Text,
        [Parameter(Mandatory = $true)][string]$Path
    )

    if ([string]::IsNullOrWhiteSpace($Text)) {
        return $false
    }

    return $Text.IndexOf($Path, [StringComparison]::OrdinalIgnoreCase) -ge 0
}

function Get-ListeningProcess {
    param([Parameter(Mandatory = $true)][int]$Port)

    $conn = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $conn) {
        return $null
    }

    return Get-CimInstance Win32_Process -Filter ("ProcessId = {0}" -f $conn.OwningProcess) -ErrorAction SilentlyContinue
}

function Get-ProcessById {
    param([Parameter(Mandatory = $true)][int]$ProcessId)
    return Get-CimInstance Win32_Process -Filter ("ProcessId = {0}" -f $ProcessId) -ErrorAction SilentlyContinue
}

function Test-GoRunModdleProcess {
    param([Parameter(Mandatory = $true)]$Process)

    $exeName = [IO.Path]::GetFileName($Process.ExecutablePath)
    if ($exeName -ne "moddle.exe") {
        return $false
    }

    $parent = Get-ProcessById -ProcessId $Process.ParentProcessId
    if (-not $parent) {
        return $false
    }

    return $parent.CommandLine -match 'go(\.exe)?"?\s+run\s+(\./|\.\\)?cmd[\\/]moddle'
}

function Clear-RepoOwnedPort {
    param([Parameter(Mandatory = $true)][int]$Port)

    $process = Get-ListeningProcess -Port $Port
    if (-not $process) {
        return
    }

    $rootPath = [IO.Path]::GetFullPath($Root).TrimEnd("\")
    $isRepoProcess = (Test-ContainsPath -Text $process.ExecutablePath -Path $rootPath) -or
        (Test-ContainsPath -Text $process.CommandLine -Path $rootPath) -or
        (Test-GoRunModdleProcess -Process $process)

    if (-not $isRepoProcess) {
        $owner = if ($process.ExecutablePath) { $process.ExecutablePath } else { $process.CommandLine }
        throw "Port $Port is already in use by PID $($process.ProcessId): $owner"
    }

    Write-Host "Stopping existing RelayChat process on port $Port (PID $($process.ProcessId))..."
    Stop-Process -Id $process.ProcessId -Force
    Start-Sleep -Milliseconds 700
}

function Start-DevWindow {
    param(
        [Parameter(Mandatory = $true)][string]$Title,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$Command
    )

    $body = @"
`$Host.UI.RawUI.WindowTitle = $(Quote-PSString $Title)
Set-Location $(Quote-PSString $WorkingDirectory)
$Command
"@

    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($body))
    Start-Process powershell -ArgumentList @("-NoExit", "-EncodedCommand", $encoded)
}

if (-not $SkipInfra) {
    Write-Host "Starting local infrastructure with docker compose..."
    Invoke-Native "docker" @("compose", "-f", $ComposeFile, "up", "-d")
}

if (-not $NoServer) {
    Clear-RepoOwnedPort -Port $ServerPort
    $serverCommand = @"
`$env:MODDLE_LISTEN = $(Quote-PSString ":$ServerPort")
`$env:MODDLE_PLUGIN_DIR = $(Quote-PSString $PluginDir)
go run ./cmd/moddle
"@
    Start-DevWindow -Title "Moddle Server" -WorkingDirectory $ServerDir -Command $serverCommand
}

if (-not $NoWeb) {
    $npm = Resolve-NpmCmd
    $webCommand = "& $(Quote-PSString $npm) run dev -- --host 127.0.0.1 --port $WebPort"
    Start-DevWindow -Title "Moddle Webapp" -WorkingDirectory $WebappDir -Command $webCommand
}

Write-Host ""
Write-Host "Dev environment requested."
Write-Host "Server: http://localhost:$ServerPort"
Write-Host "Webapp: http://localhost:$WebPort"
Write-Host "Dev login: webuser / P@ssw0rd1"
Write-Host ""
Write-Host "Use -SkipInfra when Docker services are already running."
