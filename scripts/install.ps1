# Install inari from a GitHub release archive.
#
#   iwr -useb https://raw.githubusercontent.com/hizkifw/inari/main/scripts/install.ps1 | iex
#
# Environment overrides:
#   INARI_VERSION      release tag to install (default: latest, e.g. v0.1.0)
#   INARI_INSTALL_DIR  where to put the binary (default: $env:LOCALAPPDATA\Programs\inari)
#   INARI_BASE_URL     release base, for mirrors and testing
#   GITHUB_TOKEN       raises the GitHub API rate limit, if the API is needed
#   NO_COLOR           disable colored output
#
# This runs through `iex`, so it never calls `exit`: that would close the
# caller's console. Failures surface as errors instead.
$ErrorActionPreference = 'Stop'
# The progress bar is noise here and makes Invoke-WebRequest much slower on
# Windows PowerShell 5.1.
$ProgressPreference = 'SilentlyContinue'

$repo = 'hizkifw/inari'
$baseUrl = if ($env:INARI_BASE_URL) { $env:INARI_BASE_URL } else { "https://github.com/$repo/releases" }
$installDir = if ($env:INARI_INSTALL_DIR) { $env:INARI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\inari' }
$version = $env:INARI_VERSION

# Color only when the user has not opted out. Write-Host to the console keeps
# this to the screen; piped logs then stay plain text. IsOutputRedirected can
# throw when there is no console at all, so treat that as "not a terminal".
$redirected = $true
try { $redirected = [System.Console]::IsOutputRedirected } catch { $redirected = $true }
$useColor = -not $env:NO_COLOR -and -not $redirected
# [char]27 rather than `e: Windows PowerShell 5.1, which `iex` may run under,
# does not support the `e escape.
if ($useColor) {
  $esc = [char]27
  $accent = "$esc[38;2;229;83;61m"   # torii vermilion, #E5533D
  $faint  = "$esc[38;2;117;117;117m" # #757575
  $good   = "$esc[38;2;121;201;139m" # #79C98B
  $reset  = "$esc[0m"
} else {
  $accent = ''
  $faint  = ''
  $good   = ''
  $reset  = ''
}

# This file must stay ASCII. `iwr | iex` under Windows PowerShell 5.1 does not
# decode the download as UTF-8, so every non-ASCII literal would print as a
# run of question marks. Glyphs are built from code points instead.
$bullet = [char]0x2022
$check = [char]0x2713

# ConvertFrom-Sketch turns an ASCII sketch of the torii into box-drawing
# characters: = and | are heavy lines, + a heavy cross, T a heavy down tee,
# and < > the half lines that end the top beam.
function ConvertFrom-Sketch {
  param([string]$Line)
  -join ($Line.ToCharArray() | ForEach-Object {
    switch -CaseSensitive ("$_") {
      '=' { [char]0x2501 }
      '|' { [char]0x2503 }
      '+' { [char]0x254B }
      'T' { [char]0x2533 }
      '<' { [char]0x257A }
      '>' { [char]0x2578 }
      default { $_ }
    }
  })
}

# A torii, the gate of an Inari shrine, as scripts/install.sh draws it.
function Write-Banner {
  Write-Host ''
  @(
    '<=T=====T=>',
    ' =+=====+=',
    '  |     |'
  ) | ForEach-Object { Write-Host "  $accent$(ConvertFrom-Sketch $_)$reset" }
  Write-Host "  ${faint}inari: kon in your chat$reset"
  Write-Host ''
}

# Write-Step reports work in progress; Write-Done the one successful outcome.
function Write-Step { param([string]$Message) Write-Host "  $accent$bullet$reset $Message" }
function Write-Done { param([string]$Message) Write-Host "  $good$check$reset $Message" }

function Get-Arch {
  $osArch = $null
  if ('System.Runtime.InteropServices.RuntimeInformation' -as [type]) {
    $osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
  }
  # A 32-bit PowerShell process may report x86, so check the host architecture first.
  foreach ($arch in @($osArch, $env:PROCESSOR_ARCHITEW6432, $env:PROCESSOR_ARCHITECTURE)) {
    switch ("$arch") {
      'X64' { return 'amd64' }
      'AMD64' { return 'amd64' }
      'Arm64' { return 'arm64' }
    }
  }
  throw "unsupported architecture: OSArchitecture=$osArch, PROCESSOR_ARCHITEW6432=$env:PROCESSOR_ARCHITEW6432, PROCESSOR_ARCHITECTURE=$env:PROCESSOR_ARCHITECTURE"
}

Write-Banner

# The /releases/latest redirect names the tag without API quota or a token.
# For a few minutes after a release is published GitHub serves the generic
# releases page instead, so fall back to the API, which takes GITHUB_TOKEN
# for a higher rate limit, as CI's shared runners need.
function Get-LatestVersion {
  try {
    $req = [System.Net.WebRequest]::Create("$baseUrl/latest")
    $req.Method = 'HEAD'
    $req.AllowAutoRedirect = $false
    $req.UserAgent = 'inari-installer'
    $resp = $req.GetResponse()
    $location = $resp.Headers['Location']
    $resp.Close()
    if ($location) {
      $tag = ($location.TrimEnd('/') -split '/')[-1]
      if ($tag -match '^v?\d') { return $tag }
    }
  } catch {}
  $headers = @{ 'User-Agent' = 'inari-installer'; 'Accept' = 'application/vnd.github+json' }
  if ($env:GITHUB_TOKEN) { $headers['Authorization'] = "Bearer $env:GITHUB_TOKEN" }
  $latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers $headers
  return $latest.tag_name
}

if (-not $version) {
  $version = Get-LatestVersion
}
if (-not $version.StartsWith('v')) { $version = "v$version" }

$arch = Get-Arch
$name = "inari_$($version.TrimStart('v'))_windows_$arch"
$assetUrl = "$baseUrl/download/$version"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("inari-" + [guid]::NewGuid().ToString('n'))
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
  Write-Step "downloading inari $version (windows/$arch)"
  $zipPath = Join-Path $tmp "$name.zip"
  Invoke-WebRequest -Uri "$assetUrl/$name.zip" -OutFile $zipPath -UseBasicParsing
  # GitHub serves this as octet-stream, which comes back as a byte array from
  # Invoke-WebRequest. Read it as a file so the text survives.
  $checksumPath = Join-Path $tmp 'checksums.txt'
  Invoke-WebRequest -Uri "$assetUrl/checksums.txt" -OutFile $checksumPath -UseBasicParsing
  $checksums = Get-Content -Path $checksumPath

  Write-Step 'verifying checksum'
  $expected = (
    $checksums |
      ForEach-Object { $fields = $_ -split '\s+'; if ($fields.Count -ge 2) { [pscustomobject]@{
        Hash = $fields[0]
        File = $fields[-1] -replace '^\./', ''
      } } } |
      Where-Object { $_ -and $_.File -eq "$name.zip" } |
      Select-Object -ExpandProperty Hash -First 1
  )
  if (-not $expected) { throw "no checksum for $name.zip" }

  $actual = (Get-FileHash -Algorithm SHA256 -Path $zipPath).Hash.ToLower()
  if ($actual -ne $expected.ToLower()) { throw "checksum mismatch for $name.zip" }

  Write-Step "installing inari $version"
  Expand-Archive -Path $zipPath -DestinationPath $tmp
  New-Item -ItemType Directory -Path $installDir -Force | Out-Null
  $binary = Join-Path $installDir 'inari.exe'
  # Windows refuses to overwrite a running executable but allows renaming
  # one, so a running inari is moved aside first, as inari upgrade does.
  $old = "$binary.old"
  Remove-Item -Path $old -Force -ErrorAction SilentlyContinue
  if (Test-Path $binary) { Move-Item -Path $binary -Destination $old -Force }
  Copy-Item -Path (Join-Path $tmp "$name\inari.exe") -Destination $binary -Force

  Write-Done "inari $version installed to $binary"
} finally {
  Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

# Add the install directory to the user PATH once, so new shells find inari.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$entries = @($userPath -split ';' | Where-Object { $_ })
if ($entries -notcontains $installDir) {
  $newPath = (@($entries) + $installDir) -join ';'
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  Write-Host "added $installDir to your PATH; open a new terminal to use inari"
}
$env:Path = "$env:Path;$installDir"

$config = Join-Path $env:APPDATA 'inari\config.json'
if (-not (Test-Path $config)) {
  Write-Host ''
  Write-Host "Next, write $config, starting from the example:"
  Write-Host "  https://github.com/$repo/blob/main/config.example.json"
}
Write-Host ''
Write-Host 'Upgrade later with "inari upgrade".'
