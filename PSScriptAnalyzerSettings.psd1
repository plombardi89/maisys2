# PSScriptAnalyzer settings for install.ps1 (CI's lint-powershell job). The
# script must run in Windows PowerShell 5.1, which a fresh Windows 11 has, as
# well as in PowerShell 7.
@{
    Severity = @('Error', 'Warning', 'Information')
    Rules    = @{
        PSUseCompatibleSyntax   = @{
            Enable         = $true
            TargetVersions = @('5.1', '7.0')
        }
        PSUseCompatibleCommands = @{
            Enable         = $true
            # Windows PowerShell 5.1 on Windows 10 1809, the newest Windows
            # PowerShell profile PSScriptAnalyzer has.
            TargetProfiles = @('win-48_x64_10.0.17763.0_5.1.17763.316_x64_4.0.30319.42000_framework')
        }
        PSUseCompatibleTypes    = @{
            Enable         = $true
            TargetProfiles = @('win-48_x64_10.0.17763.0_5.1.17763.316_x64_4.0.30319.42000_framework')
        }
    }
}
