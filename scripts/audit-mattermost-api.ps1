[CmdletBinding()]
param(
    [string]$Ref = "master",
    [string]$OutputJson = ""
)

$ErrorActionPreference = "Stop"

$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
$RouterPath = Join-Path $Root "server\internal\httpapi\router.go"
$SourceApi = "https://api.github.com/repos/mattermost/mattermost/contents/api/v4/source?ref=$Ref"

function Normalize-RoutePath {
    param([string]$Path)
    return ($Path -replace '\{[A-Za-z0-9_]+\}', '{param}')
}

function Add-Route {
    param(
        [hashtable]$Routes,
        [string]$Method,
        [string]$Path,
        [string]$Source
    )

    $normalizedPath = Normalize-RoutePath $Path
    $key = "$($Method.ToUpperInvariant()) $normalizedPath"
    if (-not $Routes.ContainsKey($key)) {
        $Routes[$key] = [ordered]@{
            method = $Method.ToUpperInvariant()
            path = $normalizedPath
            source = $Source
        }
    }
}

function Get-OfficialRoutes {
    $routes = @{}
    $files = Invoke-RestMethod -Headers @{ "User-Agent" = "Moddle API compatibility audit" } -Uri $SourceApi
    $yamlFiles = $files | Where-Object { $_.name -like "*.yaml" }

    foreach ($file in $yamlFiles) {
        $text = (Invoke-WebRequest -UseBasicParsing -Headers @{ "User-Agent" = "Moddle API compatibility audit" } -Uri $file.download_url).Content
        $currentPath = $null
        foreach ($line in ($text -split "`n")) {
            if ($line -match '^\s{2}"?(/api/v4/.*?)"?:\s*$') {
                $currentPath = $Matches[1]
                continue
            }
            if ($currentPath -and $line -match '^\s{4}(get|post|put|delete|patch):\s*$') {
                Add-Route -Routes $routes -Method $Matches[1] -Path $currentPath -Source $file.name
            }
        }
    }

    return $routes
}

function Get-LocalRoutes {
    $routes = @{}
    $lines = Get-Content $RouterPath
    $inApiRoute = $false
    $apiDepth = 0

    foreach ($line in $lines) {
        if (-not $inApiRoute -and $line -match 'r\.Route\("/api/v4"') {
            $inApiRoute = $true
            $apiDepth = 0
        }

        if ($line -match '\.(Get|Post|Put|Delete|Patch)\("([^"]+)"') {
            $method = $Matches[1]
            $path = $Matches[2]
            if ($inApiRoute -and -not $path.StartsWith("/api/v4")) {
                $path = "/api/v4$path"
            }
            Add-Route -Routes $routes -Method $method -Path $path -Source "router.go"
        }

        if ($line -match 'r\.Method\(http\.Method([A-Za-z]+),\s*"([^"]+)"') {
            Add-Route -Routes $routes -Method $Matches[1] -Path $Matches[2] -Source "router.go"
        }

        if ($line -match 'r\.HandleFunc\("(/api/v4/websocket)"') {
            Add-Route -Routes $routes -Method "GET" -Path $Matches[1] -Source "router.go"
        }

        if ($inApiRoute) {
            $apiDepth += ([regex]::Matches($line, '\{').Count)
            $apiDepth -= ([regex]::Matches($line, '\}').Count)
            if ($apiDepth -le 0 -and $line -match '^\s*\}\)') {
                $inApiRoute = $false
            }
        }
    }

    return $routes
}

$official = Get-OfficialRoutes
$local = Get-LocalRoutes

$officialKeys = @($official.Keys | Sort-Object)
$localKeys = @($local.Keys | Sort-Object)
$localLookup = @{}
$officialLookup = @{}
$localKeys | ForEach-Object { $localLookup[$_] = $true }
$officialKeys | ForEach-Object { $officialLookup[$_] = $true }

$matched = @($officialKeys | Where-Object { $localLookup.ContainsKey($_) })
$missing = @($officialKeys | Where-Object { -not $localLookup.ContainsKey($_) })
$extra = @($localKeys | Where-Object { -not $officialLookup.ContainsKey($_) })

$coverage = if ($officialKeys.Count -eq 0) { 0 } else { [math]::Round(($matched.Count / $officialKeys.Count) * 100, 2) }

$result = [ordered]@{
    generated_at = (Get-Date).ToString("o")
    mattermost_ref = $Ref
    official_count = $officialKeys.Count
    local_count = $localKeys.Count
    matched_count = $matched.Count
    missing_count = $missing.Count
    extra_count = $extra.Count
    coverage_percent = $coverage
    matched = $matched
    missing = $missing
    extra = $extra
}

if ($OutputJson) {
    $target = if ([System.IO.Path]::IsPathRooted($OutputJson)) { $OutputJson } else { Join-Path $Root $OutputJson }
    New-Item -ItemType Directory -Force -Path (Split-Path $target) | Out-Null
    $result | ConvertTo-Json -Depth 5 | Set-Content -Encoding UTF8 $target
}

$result | ConvertTo-Json -Depth 5
