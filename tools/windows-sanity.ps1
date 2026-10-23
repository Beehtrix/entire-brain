# Smoke-run a built entire-brain binary: version, capabilities --json, and a
# remember/recall roundtrip in a scratch git repo. Storage is isolated through
# the ENTIRE_PLUGIN_*_DIR overrides so the run neither reads nor pollutes a
# real brain. Written for the Windows release gate; the script itself is
# portable pwsh, so it can be exercised on any host with a built binary.
param(
    [Parameter(Mandatory = $true)][string]$Binary
)

$ErrorActionPreference = 'Stop'
$Binary = (Resolve-Path -LiteralPath $Binary).Path

function Invoke-Brain {
    param([string[]]$BrainArgs)
    $out = & $Binary @BrainArgs 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "entire-brain $($BrainArgs -join ' ') exited ${LASTEXITCODE}:`n$out"
    }
    $out
}

$work = Join-Path ([IO.Path]::GetTempPath()) ('brain-sanity-' + [guid]::NewGuid().ToString('n'))
foreach ($name in 'CONFIG', 'DATA', 'STATE', 'CACHE') {
    $dir = New-Item -ItemType Directory -Path (Join-Path $work $name.ToLower()) -Force
    Set-Item -Path "env:ENTIRE_PLUGIN_${name}_DIR" -Value $dir.FullName
}
$repo = (New-Item -ItemType Directory -Path (Join-Path $work 'repo')).FullName
git -C $repo init --quiet
if ($LASTEXITCODE -ne 0) { throw 'git init failed' }
git -C $repo -c user.email=ci@invalid -c user.name=ci commit --allow-empty --quiet -m scratch
if ($LASTEXITCODE -ne 0) { throw 'git commit failed' }

Push-Location $repo
try {
    $version = (Invoke-Brain @('version')).Trim()
    if (-not $version) { throw 'version printed nothing' }
    Write-Host "version: $version"

    $caps = Invoke-Brain @('capabilities', '--json') | ConvertFrom-Json
    if (-not $caps.features) { throw 'capabilities --json reported no features' }
    Write-Host "capabilities: $($caps.features -join ', ')"

    $token = 'windows_sanity_' + [guid]::NewGuid().ToString('n')
    # --path skips agent classification; no coding agent exists on a CI runner.
    Invoke-Brain @('remember', "Windows release sanity token $token", '--path', 'project.ci.windows_sanity', '--json') | Out-Null
    # Assert on the recalled facts, not the raw output: recall --json echoes the
    # query, so a bare substring match passes even when nothing was recalled.
    $recall = Invoke-Brain @('recall', $token, '--json') | ConvertFrom-Json
    $hits = @($recall.facts) | Where-Object { $_.text -match [regex]::Escape($token) }
    if (-not $hits) {
        throw "recall returned no fact carrying the remembered token (facts: $(@($recall.facts).Count))"
    }
    Write-Host 'remember/recall roundtrip ok'
}
finally {
    Pop-Location
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
Write-Host "windows sanity passed: $Binary"
