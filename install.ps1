# Install or uninstall maisys on Windows.
#
#   iex "& { $(irm https://github.com/plombardi89/maisys2/releases/latest/download/install.ps1) } [ARGS...]"
#
# Installs the latest release to %LOCALAPPDATA%\Programs\maisys\maisys.exe and
# adds that folder to your user PATH. Any ARGS are passed to "maisys sync", for
# example a config repo URL and --machine.
#
#   iex "& { $(irm https://github.com/plombardi89/maisys2/releases/latest/download/install.ps1) } -Uninstall"
#
# Removes maisys itself: the binary, its settings, state, logs and caches, and
# its PATH entry. Tools, files, packages and WSL distros that maisys installed
# are left in place.
#
# Run it as your normal user, in Windows PowerShell 5.1 or PowerShell 7.
#
# MAISYS_RELEASE_URL overrides where release files are downloaded from (used by
# tests; http, https and file URLs work). MAISYS_USER_ENV_KEY overrides the
# registry key under HKEY_CURRENT_USER that holds your user PATH (Environment),
# so that tests don't change yours.

param(
    [switch]$Uninstall,
    [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# Progress bars make Invoke-WebRequest and Expand-Archive much slower in
# Windows PowerShell.
$ProgressPreference = 'SilentlyContinue'

function Write-Note([string]$Message) {
    [Console]::Error.WriteLine("maisys-install: $Message")
}

function Show-Usage {
    [Console]::Out.Write(@'
Usage: install.ps1 [SYNC-ARGS...]
       install.ps1 -Uninstall

Installs the latest maisys release to %LOCALAPPDATA%\Programs\maisys and adds
that folder to your user PATH. Any arguments are passed to "maisys sync", for
example:

  install.ps1 https://github.com/you/maisys-config.git
  install.ps1 https://github.com/you/maisys-config.git --machine laptop

or, without saving the script first:

  iex "& { $(irm https://github.com/plombardi89/maisys2/releases/latest/download/install.ps1) } https://github.com/you/maisys-config.git"

-Uninstall removes the maisys binary, settings, state, logs and caches, and its
PATH entry. Tools, files, packages and WSL distros that maisys installed are
left in place.

'@)
}

# Assert-Platform fails unless this is Windows 11 (or Windows Server 2025) on
# amd64, the only Windows maisys is built for.
function Assert-Platform {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'install.ps1 is for Windows; on Linux, use install.sh'
    }
    # The machine's own architecture, even in an emulated or 32-bit PowerShell.
    $machine = Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment' -Name PROCESSOR_ARCHITECTURE
    $arch = $machine.PROCESSOR_ARCHITECTURE
    if ($arch -ne 'AMD64') {
        throw "unsupported architecture: $arch (maisys runs on Windows on amd64 only)"
    }
    $build = [Environment]::OSVersion.Version.Build
    if ($build -lt 22000) {
        throw "maisys needs Windows 11 (build 22000 or later), and this is Windows build $build"
    }
}

# Get-ReleaseFile downloads the release file $Name to $Destination.
function Get-ReleaseFile([string]$Name, [string]$Destination) {
    $url = $ReleaseUrl.TrimEnd('/') + '/' + $Name
    if ($url -like 'file:*') {
        Copy-Item -LiteralPath ([Uri]$url).LocalPath -Destination $Destination
        return
    }
    for ($attempt = 1; ; $attempt++) {
        try {
            Invoke-WebRequest -Uri $url -OutFile $Destination -UseBasicParsing
            return
        } catch {
            if ($attempt -ge 3) {
                throw
            }
            Start-Sleep -Seconds (2 * $attempt)
        }
    }
}

# Install-Binary puts $Source in place of maisys.exe. Windows won't overwrite
# or delete a running .exe, but it will rename one, so an existing maisys.exe
# is moved aside first, as maisys's self-update does, and deleted unless it's
# running. maisys deletes it on a later run if it is.
function Install-Binary([string]$Source) {
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $staged = Join-Path $InstallDir '.maisys.exe.new'
    Copy-Item -LiteralPath $Source -Destination $staged -Force
    if (-not (Test-Path -LiteralPath $Exe)) {
        [IO.File]::Move($staged, $Exe)
        return
    }
    $old = "$Exe.old"
    if (Test-Path -LiteralPath $old) {
        try {
            Remove-Item -LiteralPath $old -Force
        } catch {
            # An earlier maisys.exe.old is still running.
            $old = "$Exe.old-" + [Guid]::NewGuid().ToString('N').Substring(0, 8)
        }
    }
    [IO.File]::Move($Exe, $old)
    try {
        [IO.File]::Move($staged, $Exe)
    } catch {
        [IO.File]::Move($old, $Exe)
        throw
    }
    Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
}

# Test-SameDirectory reports whether the PATH entry $Entry is the folder $Dir.
function Test-SameDirectory([string]$Entry, [string]$Dir) {
    $expanded = [Environment]::ExpandEnvironmentVariables($Entry.Trim().Trim('"'))
    return $expanded.TrimEnd('\') -eq $Dir.TrimEnd('\')
}

# Send-SettingChange tells running programs, such as Explorer, that the user's
# environment changed, so programs started from now on get the new PATH.
function Send-SettingChange {
    try {
        if (-not ('MaisysInstall.NativeMethods' -as [type])) {
            Add-Type -Namespace MaisysInstall -Name NativeMethods -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
        }
        $result = [UIntPtr]::Zero
        $broadcast = [IntPtr]0xffff
        $settingChange = 0x1A
        $abortIfHung = 2
        [void][MaisysInstall.NativeMethods]::SendMessageTimeout($broadcast, $settingChange, [UIntPtr]::Zero, 'Environment', $abortIfHung, 5000, [ref]$result)
    } catch {
        Write-Note "note: couldn't tell Windows that your PATH changed ($($_.Exception.Message)); sign out and back in if new terminals don't see the change"
    }
}

# Edit-UserPath adds the folder $Dir to the user PATH, or with -Remove takes it
# out, and reports whether that changed it. The PATH is read and written as
# stored in the registry, so the variables in other entries, such as
# %USERPROFILE%, are kept.
function Edit-UserPath([string]$Dir, [switch]$Remove) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($EnvKey)
    try {
        $exists = $key.GetValueNames() -contains 'Path'
        $path = ''
        $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        if ($exists) {
            $path = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
            $kind = $key.GetValueKind('Path')
        }
        $entries = @($path -split ';')
        $kept = @($entries | Where-Object { $_ -eq '' -or -not (Test-SameDirectory $_ $Dir) })
        if ($Remove) {
            if ($kept.Count -eq $entries.Count) {
                return $false
            }
            $key.SetValue('Path', ($kept -join ';'), $kind)
        } else {
            if ($kept.Count -ne $entries.Count) {
                return $false
            }
            if ($path -eq '') {
                $path = $Dir
            } elseif ($path.EndsWith(';')) {
                $path += $Dir
            } else {
                $path += ";$Dir"
            }
            $key.SetValue('Path', $path, $kind)
        }
    } finally {
        $key.Close()
    }
    Send-SettingChange
    return $true
}

function Invoke-Install {
    Assert-Platform
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('maisys-install-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $sums = Join-Path $tmp 'checksums.txt'
        try {
            Get-ReleaseFile 'checksums.txt' $sums
        } catch {
            throw "couldn't download checksums.txt from ${ReleaseUrl}: $($_.Exception.Message)"
        }
        $asset = ''
        $want = ''
        foreach ($line in [IO.File]::ReadAllLines($sums)) {
            $fields = @($line.Trim() -split '\s+')
            if ($fields.Count -eq 2 -and $fields[1] -like 'maisys_*_windows_amd64.zip') {
                $want = $fields[0]
                $asset = $fields[1]
            }
        }
        if ($asset -eq '') {
            throw 'the release has no windows_amd64 archive'
        }
        $version = $asset -replace '^maisys_', '' -replace '_windows_amd64\.zip$', ''

        Write-Note "downloading maisys $version"
        $zip = Join-Path $tmp $asset
        try {
            Get-ReleaseFile $asset $zip
        } catch {
            throw "couldn't download ${asset}: $($_.Exception.Message)"
        }
        $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash.ToLowerInvariant()
        if ($got -ne $want.ToLowerInvariant()) {
            throw "checksum mismatch for $asset (expected $want, got $got)"
        }
        $unpacked = Join-Path $tmp 'unpacked'
        try {
            Expand-Archive -LiteralPath $zip -DestinationPath $unpacked
        } catch {
            throw "couldn't unpack ${asset}: $($_.Exception.Message)"
        }
        $new = Join-Path $unpacked 'maisys.exe'
        if (-not (Test-Path -LiteralPath $new -PathType Leaf)) {
            throw "$asset has no maisys.exe"
        }
        Install-Binary $new
        Write-Note "installed maisys $version to $Exe"
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    if (Edit-UserPath $InstallDir) {
        Write-Note "added $InstallDir to your user PATH; terminals that were already open need restarting to find maisys"
    }
    # This PowerShell finds it straight away.
    $current = [string]$env:Path
    if (-not (@($current -split ';') | Where-Object { $_ -ne '' -and (Test-SameDirectory $_ $InstallDir) })) {
        $env:Path = $current.TrimEnd(';') + ";$InstallDir"
    }
}

function Invoke-Uninstall {
    $removed = $false
    $paths = @($Exe, (Join-Path $InstallDir '.maisys.exe.new'))
    if (Test-Path -LiteralPath $InstallDir) {
        $paths += @(Get-ChildItem -LiteralPath $InstallDir -Filter 'maisys.exe.old*' -Force | ForEach-Object { $_.FullName })
    }
    $paths += @($ConfigDir, $StateDir)
    foreach ($path in $paths) {
        if (Test-Path -LiteralPath $path) {
            try {
                Remove-Item -LiteralPath $path -Recurse -Force
            } catch {
                throw "couldn't remove ${path} (is maisys running?): $($_.Exception.Message)"
            }
            Write-Note "removed $path"
            $removed = $true
        }
    }
    if ((Test-Path -LiteralPath $InstallDir) -and -not (Get-ChildItem -LiteralPath $InstallDir -Force)) {
        Remove-Item -LiteralPath $InstallDir -Force
    }
    if (Edit-UserPath $InstallDir -Remove) {
        Write-Note "removed $InstallDir from your user PATH"
        $removed = $true
    }

    if ($removed) {
        Write-Note 'done; tools, files, packages and WSL distros that maisys installed are still in place'
    } else {
        Write-Note "maisys isn't installed; nothing to remove"
    }
}

try {
    $syncArgs = @($args)
    if ($Help -or $syncArgs -contains '--help') {
        Show-Usage
        return
    }

    $ReleaseUrl = $env:MAISYS_RELEASE_URL
    if (-not $ReleaseUrl) {
        $ReleaseUrl = 'https://github.com/plombardi89/maisys2/releases/latest/download'
    }
    $EnvKey = $env:MAISYS_USER_ENV_KEY
    if (-not $EnvKey) {
        $EnvKey = 'Environment'
    }
    if (-not $env:LOCALAPPDATA -or -not $env:APPDATA) {
        throw "can't find where to install maisys: LOCALAPPDATA or APPDATA isn't set"
    }
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\maisys'
    $Exe = Join-Path $InstallDir 'maisys.exe'
    $ConfigDir = Join-Path $env:APPDATA 'maisys'
    $StateDir = Join-Path $env:LOCALAPPDATA 'maisys'

    if ($Uninstall -or $syncArgs -contains '--uninstall') {
        if (@($syncArgs | Where-Object { $_ -ne '--uninstall' }).Count -gt 0) {
            throw '-Uninstall takes no other arguments'
        }
        Invoke-Uninstall
        return
    }

    Invoke-Install
    if ($syncArgs.Count -eq 0) {
        Write-Note "next: run 'maisys sync CONFIG-URL' to set up this machine, or 'maisys config init' to start a new config repo"
        return
    }
    Write-Note "running maisys sync $($syncArgs -join ' ')"
    # maisys runs in this console, so it can ask for confirmation. Where
    # Windows PowerShell's own error output is redirected, it turns what
    # maisys writes there into errors, which mustn't stop this script.
    $ErrorActionPreference = 'Continue'
    & $Exe sync @syncArgs
    if ($PSCommandPath) {
        exit $LASTEXITCODE
    }
} catch {
    Write-Note "error: $($_.Exception.Message)"
    # Run as a file, fail with exit code 1. Run with iex, return to the prompt
    # rather than close the window.
    if ($PSCommandPath) {
        exit 1
    }
}
