[CmdletBinding()]
param(
    [switch]$SkipInfra,
    [switch]$NoServer,
    [switch]$NoWeb,
    [int]$WebPort = 5173
)

$ErrorActionPreference = "Stop"

$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
$ServerDir = Join-Path $Root "server"
$WebappDir = Join-Path $Root "webapp"
$ComposeFile = Join-Path $Root "deploy\docker\compose.dev.yaml"
$ServerPort = 8065

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

function Test-GoRunMoyroProcess {
    param([Parameter(Mandatory = $true)]$Process)

    $exeName = [IO.Path]::GetFileName($Process.ExecutablePath)
    if ($exeName -ne "moyro.exe") {
        return $false
    }

    $parent = Get-ProcessById -ProcessId $Process.ParentProcessId
    if (-not $parent) {
        return $false
    }

    return $parent.CommandLine -match 'go(\.exe)?"?\s+run\s+(\./|\.\\)?cmd[\\/]moyro'
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
        (Test-GoRunMoyroProcess -Process $process)

    if (-not $isRepoProcess) {
        $owner = if ($process.ExecutablePath) { $process.ExecutablePath } else { $process.CommandLine }
        throw "Port $Port is already in use by PID $($process.ProcessId): $owner"
    }

    Write-Host "Stopping existing moyro process on port $Port (PID $($process.ProcessId))..."
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
`$env:POSTGRES_DSN = $(Quote-PSString "postgres://moyro:moyro@localhost:5433/moyro?sslmode=disable")
`$env:BOOTSTRAP_ADMIN = $(Quote-PSString "admin@moyro.local")
`$env:BOOTSTRAP_ADMIN_PASSWORD = $(Quote-PSString "MoyroDev!2026")
`$env:ENCRYPTION_KEY = $(Quote-PSString "bW95cm8tZGV2LWVuY3J5cHRpb24ta2V5LTMyLWJ5dGU=")
go run ./cmd/moyro
"@
    Start-DevWindow -Title "moyro Server" -WorkingDirectory $ServerDir -Command $serverCommand
}

if (-not $NoWeb) {
    $npm = Resolve-NpmCmd
    $webCommand = "& $(Quote-PSString $npm) run dev -- --host 127.0.0.1 --port $WebPort"
    Start-DevWindow -Title "moyro Webapp" -WorkingDirectory $WebappDir -Command $webCommand
}

Write-Host ""
Write-Host "Dev environment requested."
Write-Host "Server: http://localhost:$ServerPort"
Write-Host "Webapp: http://localhost:$WebPort"
Write-Host "Bootstrap admin: admin@moyro.local / MoyroDev!2026"
Write-Host ""
Write-Host "Use -SkipInfra when Docker services are already running."
