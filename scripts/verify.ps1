[CmdletBinding()]
param(
  [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")

function Write-Step {
  param([string]$Message)
  Write-Host ""
  Write-Host "==> $Message" -ForegroundColor Cyan
}

function Resolve-NpmCmd {
  $preferred = "C:\Program Files\nodejs\npm.cmd"
  if (Test-Path $preferred) {
    return $preferred
  }

  $npmCmd = Get-Command npm.cmd -ErrorAction SilentlyContinue
  if ($npmCmd) {
    return $npmCmd.Source
  }

  $npm = Get-Command npm -ErrorAction SilentlyContinue
  if ($npm) {
    return $npm.Source
  }

  throw "npm was not found on PATH"
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

$npm = Resolve-NpmCmd

Write-Step "web typecheck"
Push-Location (Join-Path $root "webapp")
try {
  Invoke-Native $npm @("run", "typecheck")

  if (-not $SkipBuild) {
    Write-Step "web production build"
    Invoke-Native $npm @("run", "build")
  }
} finally {
  Pop-Location
}

Write-Step "server tests"
$goCache = Join-Path $root ".cache\go-build"
New-Item -ItemType Directory -Force -Path $goCache | Out-Null
$env:GOCACHE = $goCache
Push-Location (Join-Path $root "server")
try {
  Invoke-Native "go" @("test", "./...")
} finally {
  Pop-Location
}

Write-Host ""
Write-Host "All verification checks passed." -ForegroundColor Green
