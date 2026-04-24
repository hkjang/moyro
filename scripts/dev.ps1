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
    $serverCommand = @"
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
Write-Host "Server: http://localhost:8065"
Write-Host "Webapp: http://localhost:$WebPort"
Write-Host "Dev login: webuser / P@ssw0rd1"
Write-Host ""
Write-Host "Use -SkipInfra when Docker services are already running."
