<#
.SYNOPSIS
  Nodit CLI installer for Windows.

.DESCRIPTION
  irm https://raw.githubusercontent.com/noditlabs/nodit-cli/main/scripts/install.ps1 | iex

  The script is piped into a shell, so it reads the archive checksum from the release and refuses
  to install a binary that does not match. Environment overrides:

    NODIT_VERSION          tag to install, such as v0.1.0 (default: the latest release)
    NODIT_INSTALL_DIR      where the binary lands (default: %LOCALAPPDATA%\Nodit\bin)
    NODIT_NO_MODIFY_PATH   set to 1 to leave the user PATH alone
#>

$ErrorActionPreference = 'Stop'

# Read before the overwrite, so the summary can name what is being replaced. An older or unreadable
# binary leaves this empty, which must not stop the install.
function Get-NoditVersion($path) {
    if (-not (Test-Path $path)) { return $null }
    try { (& $path version --output json 2>$null | ConvertFrom-Json).data.version }
    catch { $null }
}

$repo = 'noditlabs/nodit-cli'
$version = $env:NODIT_VERSION
$installDir = if ($env:NODIT_INSTALL_DIR) { $env:NODIT_INSTALL_DIR }
              else { Join-Path $env:LOCALAPPDATA 'Nodit\bin' }

# The release publishes windows/amd64 only, so an arm64 host would otherwise install a binary it
# cannot run. Windows on arm64 emulates x64, but say so rather than pretending it is native.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') {
    Write-Host "No native windows/$arch build is published; installing the amd64 build." -ForegroundColor Yellow
}

if (-not $version) {
    $release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
    $version = $release.tag_name
    if (-not $version) { throw 'Cannot determine the latest release. Set NODIT_VERSION to a tag.' }
}

$name = 'nodit-windows-amd64'
$base = "https://github.com/$repo/releases/download/$version"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    Write-Host "Downloading $name $version"
    $zip = Join-Path $tmp "$name.zip"
    Invoke-WebRequest "$base/$name.zip" -OutFile $zip
    $sums = Join-Path $tmp 'checksums.txt'
    Invoke-WebRequest "$base/checksums.txt" -OutFile $sums

    $actual = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()

    # checksums.txt lists names as ./nodit-windows-amd64.zip because sha256sum ./* produced it.
    $line = Select-String -Path $sums -Pattern "^([0-9a-f]{64})\s+\.?/?$([regex]::Escape($name)).zip$"
    if (-not $line) { throw "checksums.txt has no entry for $name.zip" }
    $expected = $line.Matches[0].Groups[1].Value

    if ($actual -ne $expected) {
        throw "Checksum mismatch for $name.zip - refusing to install."
    }

    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $binary = Join-Path $tmp "$name\nodit.exe"
    if (-not (Test-Path $binary)) { throw "The archive does not contain $name\nodit.exe" }

    # Run it before it replaces anything. A binary that cannot start here would otherwise be
    # reported as installed, and a working one would already have been overwritten.
    & $binary version --output json > $null 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "The downloaded binary does not run on this machine; nothing was changed."
    }

    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    $target = Join-Path $installDir 'nodit.exe'
    $previous = Get-NoditVersion $target

    # Windows locks a running executable, but still allows renaming it. The old file is moved aside
    # instead of overwritten; a running process keeps it, and the next install clears the leftover.
    $retired = "$target.old"
    Remove-Item $retired -Force -ErrorAction SilentlyContinue
    if (Test-Path $target) {
        try { Move-Item $target $retired -Force }
        catch { throw "Cannot replace $target - close any running nodit and try again." }
    }
    try {
        Copy-Item $binary $target -Force
    }
    catch {
        if (Test-Path $retired) { Move-Item $retired $target -Force }
        throw
    }
    Remove-Item $retired -Force -ErrorAction SilentlyContinue

    $current = Get-NoditVersion $target
    if (-not $current) { $current = $version }
    if (-not $previous) { Write-Host "Installed nodit $current at $target" }
    elseif ($previous -eq $current) { Write-Host "Reinstalled nodit $current at $target" }
    else { Write-Host "Updated nodit $previous -> $current at $target" }
    Write-Host 'Start with: nodit auth login, or nodit --help'

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($userPath -split ';' -contains $installDir) {
        Write-Host 'Already on PATH. Open a new terminal to pick it up.'
    }
    elseif ($env:NODIT_NO_MODIFY_PATH -eq '1') {
        Write-Host "Add it to PATH:  setx PATH `"$installDir;%PATH%`"" -ForegroundColor Yellow
    }
    else {
        # The user PATH only; the machine PATH needs elevation and affects every account.
        $updated = if ($userPath) { "$userPath;$installDir" } else { $installDir }
        [Environment]::SetEnvironmentVariable('Path', $updated, 'User')
        Write-Host "Added $installDir to the user PATH. Open a new terminal to pick it up."
    }
}
finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
