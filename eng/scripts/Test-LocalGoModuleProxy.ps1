# Copyright (c) Microsoft Corporation. All rights reserved.
# Licensed under the MIT License.

#Requires -Version 7.0

<#
.SYNOPSIS
Rehearses consumption of the Windows native driver through a local Go proxy.

.DESCRIPTION
Packages a supplied windows/amd64 driver module using the Go module proxy
protocol, then builds and runs an isolated cgo consumer without Cargo or Rust
on PATH. The consumer uses only the file-based proxy and never uses a replace
directive.

The rehearsal writes evidence.json beneath a deterministic temporary work
directory. Successful work directories are removed unless
-KeepWorkDirectory is supplied. Failed work directories are retained so the
partial evidence and generated proxy can be inspected; rerunning the same
module and version cleans that directory first.

.PARAMETER ModuleDirectory
Directory containing the windows/amd64 driver module's go.mod and native
archive.

.PARAMETER Version
Canonical Go module semantic version to publish only into the local proxy.

.PARAMETER WorkRoot
Root for deterministic temporary rehearsal directories.

.PARAMETER KeepWorkDirectory
Retains the work directory after a successful rehearsal.

.PARAMETER SelfTest
Runs fixture checks for uppercase proxy path escaping, escaped version file
names, and ZIP entry structure without building the native consumer.

.EXAMPLE
pwsh eng/scripts/Test-LocalGoModuleProxy.ps1 `
  -ModuleDirectory C:\src\azure-cosmos-driver\windows\amd64 `
  -Version v0.1.0 `
  -KeepWorkDirectory

.EXAMPLE
pwsh eng/scripts/Test-LocalGoModuleProxy.ps1 -SelfTest
#>
[CmdletBinding(DefaultParameterSetName = 'Rehearsal')]
param(
    [Parameter(Mandatory = $true, ParameterSetName = 'Rehearsal')]
    [string]$ModuleDirectory,

    [Parameter(Mandatory = $true, ParameterSetName = 'Rehearsal')]
    [string]$Version,

    [Parameter(Mandatory = $true, ParameterSetName = 'SelfTest')]
    [switch]$SelfTest,

    [string]$WorkRoot = (Join-Path ([IO.Path]::GetTempPath()) 'azure-cosmos-driver-go-proxy'),

    [Parameter(ParameterSetName = 'Rehearsal')]
    [string]$GoExecutable = 'go',

    [Parameter(ParameterSetName = 'Rehearsal')]
    [string]$CCompiler = 'gcc',

    [Parameter(ParameterSetName = 'Rehearsal')]
    [string]$ObjectDumpExecutable,

    [switch]$KeepWorkDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$expectedModulePath = 'github.com/Azure/azure-cosmos-driver/windows/amd64'
$repositoryRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$utf8NoBom = [Text.UTF8Encoding]::new($false)

Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

public static class PhysicalPath
{
    private const uint FileFlagBackupSemantics = 0x02000000;
    private const uint OpenExisting = 3;

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern SafeFileHandle CreateFileW(
        string fileName,
        uint desiredAccess,
        FileShare shareMode,
        IntPtr securityAttributes,
        uint creationDisposition,
        uint flagsAndAttributes,
        IntPtr templateFile);

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern uint GetFinalPathNameByHandleW(
        SafeFileHandle file,
        char[] filePath,
        uint filePathLength,
        uint flags);

    public static string Resolve(string path)
    {
        using (SafeFileHandle handle = CreateFileW(
            path,
            0,
            FileShare.ReadWrite | FileShare.Delete,
            IntPtr.Zero,
            OpenExisting,
            FileFlagBackupSemantics,
            IntPtr.Zero))
        {
            if (handle.IsInvalid)
            {
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot resolve physical path: " + path);
            }

            char[] buffer = new char[32768];
            uint length = GetFinalPathNameByHandleW(handle, buffer, (uint)buffer.Length, 0);
            if (length == 0 || length >= buffer.Length)
            {
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot resolve physical path: " + path);
            }

            string resolved = new string(buffer, 0, (int)length);
            if (resolved.StartsWith(@"\\?\UNC\", StringComparison.OrdinalIgnoreCase))
            {
                return @"\\" + resolved.Substring(8);
            }
            if (resolved.StartsWith(@"\\?\", StringComparison.OrdinalIgnoreCase))
            {
                return resolved.Substring(4);
            }
            return resolved;
        }
    }
}
'@

function Assert-Condition {
    param(
        [Parameter(Mandatory = $true)]
        [bool]$Condition,

        [Parameter(Mandatory = $true)]
        [string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function Write-Utf8File {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [AllowEmptyString()]
        [string]$Content
    )

    [IO.File]::WriteAllText($Path, $Content, $utf8NoBom)
}

function Get-Sha256Text {
    param(
        [Parameter(Mandatory = $true)]
        [AllowEmptyString()]
        [string]$Text
    )

    $bytes = $utf8NoBom.GetBytes($Text)
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        $hash = $sha256.ComputeHash($bytes)
    }
    finally {
        $sha256.Dispose()
    }
    ([BitConverter]::ToString($hash) -replace '-', '').ToLowerInvariant()
}

function Get-FileSha256 {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function ConvertTo-GoProxyEscapedString {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Value
    )

    $builder = [Text.StringBuilder]::new()
    foreach ($character in $Value.ToCharArray()) {
        if ($character -ge 'A' -and $character -le 'Z') {
            [void]$builder.Append('!')
            [void]$builder.Append([char]::ToLowerInvariant($character))
        }
        else {
            [void]$builder.Append($character)
        }
    }
    $builder.ToString()
}

function Get-ModulePath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$GoModPath
    )

    $goMod = [IO.File]::ReadAllText($GoModPath)
    $match = [regex]::Match($goMod, '(?m)^\s*module\s+(\S+)\s*$')
    Assert-Condition $match.Success "go.mod does not contain a valid module directive: $GoModPath"
    $match.Groups[1].Value
}

function Get-GoDirective {
    param(
        [Parameter(Mandatory = $true)]
        [string]$GoModPath
    )

    $goMod = [IO.File]::ReadAllText($GoModPath)
    $match = [regex]::Match($goMod, '(?m)^\s*go\s+(\S+)\s*$')
    Assert-Condition $match.Success "go.mod does not contain a valid go directive: $GoModPath"
    $match.Groups[1].Value
}

function Assert-CanonicalVersion {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Value
    )

    $pattern = '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z](?:[0-9A-Za-z.-]*[0-9A-Za-z])?)?$'
    Assert-Condition ($Value -cmatch $pattern) "Version '$Value' is not a canonical Go module semantic version."
}

function Get-SourceFiles {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Root
    )

    $items = @(Get-ChildItem -LiteralPath $Root -Recurse -Force)
    $reparsePoint = $items |
        Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
        Select-Object -First 1
    if ($null -ne $reparsePoint) {
        throw "Module ZIP input must not contain symbolic links or reparse points: $($reparsePoint.FullName)"
    }
    $files = @($items | Where-Object { -not $_.PSIsContainer })
    Assert-Condition ($files.Count -gt 0) "Module directory is empty: $Root"
    @($files | Sort-Object {
        [IO.Path]::GetRelativePath($Root, $_.FullName).Replace('\', '/')
    })
}

function Get-ModuleTreeEvidence {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Root,

        [Parameter(Mandatory = $true)]
        [IO.FileInfo[]]$Files
    )

    $entries = @(
        foreach ($file in $Files) {
            $relativePath = [IO.Path]::GetRelativePath($Root, $file.FullName).Replace('\', '/')
            [ordered]@{
                path = $relativePath
                size = $file.Length
                sha256 = Get-FileSha256 $file.FullName
            }
        }
    )
    $manifestText = (($entries | ForEach-Object { "$($_.sha256)  $($_.path)" }) -join "`n") + "`n"
    [ordered]@{
        sha256 = Get-Sha256Text $manifestText
        files = $entries
    }
}

function New-ModuleZip {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ModuleRoot,

        [Parameter(Mandatory = $true)]
        [IO.FileInfo[]]$SourceFiles,

        [Parameter(Mandatory = $true)]
        [string]$ModulePath,

        [Parameter(Mandatory = $true)]
        [string]$ModuleVersion,

        [Parameter(Mandatory = $true)]
        [string]$ZipPath
    )

    $prefix = "$ModulePath@$ModuleVersion/"
    $zipStream = [IO.File]::Open($ZipPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
    try {
        $archive = [IO.Compression.ZipArchive]::new(
            $zipStream,
            [IO.Compression.ZipArchiveMode]::Create,
            $false
        )
        try {
            foreach ($file in $SourceFiles) {
                $relativePath = [IO.Path]::GetRelativePath($ModuleRoot, $file.FullName).Replace('\', '/')
                $entry = $archive.CreateEntry(
                    "$prefix$relativePath",
                    [IO.Compression.CompressionLevel]::Optimal
                )
                $entry.LastWriteTime = [DateTimeOffset]::new(1980, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
                $inputStream = $file.OpenRead()
                try {
                    $outputStream = $entry.Open()
                    try {
                        $inputStream.CopyTo($outputStream)
                    }
                    finally {
                        $outputStream.Dispose()
                    }
                }
                finally {
                    $inputStream.Dispose()
                }
            }
        }
        finally {
            $archive.Dispose()
        }
    }
    finally {
        $zipStream.Dispose()
    }
}

function Assert-ModuleZip {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ZipPath,

        [Parameter(Mandatory = $true)]
        [string]$ModulePath,

        [Parameter(Mandatory = $true)]
        [string]$ModuleVersion
    )

    $prefix = "$ModulePath@$ModuleVersion/"
    $zipStream = [IO.File]::OpenRead($ZipPath)
    try {
        $archive = [IO.Compression.ZipArchive]::new(
            $zipStream,
            [IO.Compression.ZipArchiveMode]::Read,
            $false
        )
        try {
            $entryNames = @($archive.Entries | ForEach-Object FullName)
            Assert-Condition ($entryNames.Count -gt 0) "Module ZIP has no entries: $ZipPath"
            Assert-Condition ($entryNames -ccontains "${prefix}go.mod") "Module ZIP does not contain ${prefix}go.mod."
            foreach ($entryName in $entryNames) {
                Assert-Condition (-not $entryName.Contains('\')) "Module ZIP entry uses a backslash: $entryName"
                Assert-Condition $entryName.StartsWith($prefix, [StringComparison]::Ordinal) "Module ZIP entry has an invalid prefix: $entryName"
                Assert-Condition ($entryName.Length -gt $prefix.Length) "Module ZIP contains an empty root entry: $entryName"
            }
            $entryNames
        }
        finally {
            $archive.Dispose()
        }
    }
    finally {
        $zipStream.Dispose()
    }
}

function New-LocalGoProxy {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ModuleRoot,

        [Parameter(Mandatory = $true)]
        [IO.FileInfo[]]$SourceFiles,

        [Parameter(Mandatory = $true)]
        [string]$ModulePath,

        [Parameter(Mandatory = $true)]
        [string]$ModuleVersion,

        [Parameter(Mandatory = $true)]
        [string]$ProxyRoot
    )

    $escapedPath = ConvertTo-GoProxyEscapedString $ModulePath
    $escapedVersion = ConvertTo-GoProxyEscapedString $ModuleVersion
    $versionDirectory = Join-Path $ProxyRoot ($escapedPath.Replace('/', [IO.Path]::DirectorySeparatorChar)) '@v'
    New-Item -ItemType Directory -Path $versionDirectory -Force | Out-Null

    $listPath = Join-Path $versionDirectory 'list'
    $infoPath = Join-Path $versionDirectory "$escapedVersion.info"
    $modPath = Join-Path $versionDirectory "$escapedVersion.mod"
    $zipPath = Join-Path $versionDirectory "$escapedVersion.zip"

    Write-Utf8File $listPath "$ModuleVersion`n"
    $info = [ordered]@{
        Version = $ModuleVersion
        Time = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    }
    Write-Utf8File $infoPath (($info | ConvertTo-Json -Compress) + "`n")
    [IO.File]::Copy((Join-Path $ModuleRoot 'go.mod'), $modPath, $true)
    New-ModuleZip $ModuleRoot $SourceFiles $ModulePath $ModuleVersion $zipPath
    $entries = @(Assert-ModuleZip $zipPath $ModulePath $ModuleVersion)

    [ordered]@{
        escapedModulePath = $escapedPath
        escapedVersion = $escapedVersion
        versionDirectory = $versionDirectory
        listPath = $listPath
        infoPath = $infoPath
        modPath = $modPath
        zipPath = $zipPath
        zipSha256 = Get-FileSha256 $zipPath
        zipEntries = $entries
    }
}

function Resolve-Application {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$Description
    )

    $command = Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    Assert-Condition ($null -ne $command) "$Description '$Name' is not available."
    $command.Source
}

function Format-CommandLine {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,

        [Parameter(Mandatory = $true)]
        [AllowEmptyCollection()]
        [string[]]$ArgumentList
    )

    $formattedArguments = foreach ($argument in $ArgumentList) {
        if ($argument -match '[\s"]') {
            '"' + $argument.Replace('"', '\"') + '"'
        }
        else {
            $argument
        }
    }
    (@($FilePath) + $formattedArguments) -join ' '
}

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,

        [AllowEmptyCollection()]
        [string[]]$ArgumentList = @(),

        [Parameter(Mandatory = $true)]
        [string]$WorkingDirectory,

        [hashtable]$Environment,

        [Parameter(Mandatory = $true)]
        [AllowEmptyCollection()]
        [Collections.Generic.List[object]]$CommandEvidence,

        [switch]$AllowFailure,

        [switch]$OmitOutputFromEvidence
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.WorkingDirectory = $WorkingDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in $ArgumentList) {
        [void]$startInfo.ArgumentList.Add($argument)
    }
    if ($null -ne $Environment) {
        $startInfo.Environment.Clear()
        foreach ($name in $Environment.Keys) {
            $startInfo.Environment[$name] = [string]$Environment[$name]
        }
    }

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        Assert-Condition $process.Start() "Failed to start process: $FilePath"
        $standardOutputTask = $process.StandardOutput.ReadToEndAsync()
        $standardErrorTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $standardOutput = $standardOutputTask.GetAwaiter().GetResult().TrimEnd()
        $standardError = $standardErrorTask.GetAwaiter().GetResult().TrimEnd()
        $commandLine = Format-CommandLine $FilePath $ArgumentList
        $result = [ordered]@{
            command = $commandLine
            workingDirectory = $WorkingDirectory
            exitCode = $process.ExitCode
            stdout = $standardOutput
            stderr = $standardError
        }
        $evidenceResult = [ordered]@{
            command = $commandLine
            workingDirectory = $WorkingDirectory
            exitCode = $process.ExitCode
            stdout = if ($OmitOutputFromEvidence) { '<omitted after parsing>' } else { $standardOutput }
            stderr = $standardError
        }
        $CommandEvidence.Add($evidenceResult)
        if ($process.ExitCode -ne 0 -and -not $AllowFailure) {
            $details = @($standardOutput, $standardError) | Where-Object { $_ }
            throw "Command failed with exit code $($process.ExitCode): $commandLine`n$($details -join "`n")"
        }
        [pscustomobject]$result
    }
    finally {
        $process.Dispose()
    }
}

function ConvertTo-FileProxyUri {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $uri = [Uri]::new(([IO.Path]::GetFullPath($Path) + [IO.Path]::DirectorySeparatorChar)).AbsoluteUri.TrimEnd('/')
    $uri = $uri.Replace(',', '%2C').Replace('|', '%7C')
    Assert-Condition ($uri -notmatch '[,|]') "File proxy URI contains a Go proxy-list separator: $uri"
    $uri
}

function Format-GoToolCommand {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    Assert-Condition (-not $Path.Contains('"')) "Tool path contains an unsupported quote: $Path"
    if ($Path -match '\s') {
        return "`"$Path`""
    }
    $Path
}

function Get-IsolatedEnvironment {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ProxyRoot,

        [Parameter(Mandatory = $true)]
        [string]$ConsumerRoot,

        [Parameter(Mandatory = $true)]
        [string]$GoPath,

        [Parameter(Mandatory = $true)]
        [string]$CompilerPath,

        [Parameter(Mandatory = $true)]
        [string]$ObjectDumpPath
    )

    $environment = @{}
    foreach ($entry in [Environment]::GetEnvironmentVariables().GetEnumerator()) {
        $environment[[string]$entry.Key] = [string]$entry.Value
    }

    $pathDirectories = @(
        [IO.Path]::GetDirectoryName($GoPath)
        [IO.Path]::GetDirectoryName($CompilerPath)
        [IO.Path]::GetDirectoryName($ObjectDumpPath)
        (Join-Path $env:SystemRoot 'System32')
        $env:SystemRoot
    ) | Select-Object -Unique

    $proxyUri = ConvertTo-FileProxyUri $ProxyRoot
    $environment['PATH'] = $pathDirectories -join [IO.Path]::PathSeparator
    $environment['CC'] = Format-GoToolCommand $CompilerPath
    $environment['CGO_ENABLED'] = '1'
    $environment['GOARCH'] = 'amd64'
    $environment['GO111MODULE'] = 'on'
    $environment['GOCACHE'] = Join-Path $ConsumerRoot 'go-build-cache'
    $environment['GOENV'] = 'off'
    $environment['GOFLAGS'] = ''
    $environment['GOMODCACHE'] = Join-Path $ConsumerRoot 'go-mod-cache'
    $environment['GONOPROXY'] = ''
    $environment['GONOSUMDB'] = ''
    $environment['GOPATH'] = Join-Path $ConsumerRoot 'gopath'
    $environment['GOPRIVATE'] = ''
    $environment['GOPROXY'] = $proxyUri
    $environment['GOSUMDB'] = 'off'
    $environment['GOTOOLCHAIN'] = 'local'
    $environment['GOVCS'] = '*:off'
    $environment['GOWORK'] = 'off'
    $environment['GOOS'] = 'windows'
    foreach ($name in @('CARGO', 'CARGO_HOME', 'RUSTC', 'RUSTUP_HOME')) {
        [void]$environment.Remove($name)
    }

    [ordered]@{
        values = $environment
        pathDirectories = $pathDirectories
        proxyUri = $proxyUri
    }
}

function Get-OptionalProvenance {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ModuleRoot
    )

    $directory = [IO.DirectoryInfo]::new($ModuleRoot)
    for ($depth = 0; $depth -le 3 -and $null -ne $directory; $depth++) {
        $candidate = Join-Path $directory.FullName 'provenance.json'
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return [ordered]@{
                path = $candidate
                sha256 = Get-FileSha256 $candidate
                content = Get-Content -LiteralPath $candidate -Raw | ConvertFrom-Json
            }
        }
        $directory = $directory.Parent
    }
    $null
}

function Get-GitProvenance {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ModuleRoot,

        [Parameter(Mandatory = $true)]
        [AllowEmptyCollection()]
        [Collections.Generic.List[object]]$CommandEvidence
    )

    $git = Get-Command git -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $git) {
        return $null
    }

    $gitArguments = @('--no-optional-locks', '-C', $ModuleRoot)
    $inside = Invoke-CapturedProcess $git.Source ($gitArguments + @('rev-parse', '--is-inside-work-tree')) $ModuleRoot $null $CommandEvidence -AllowFailure
    if ($inside.ExitCode -ne 0 -or $inside.Stdout -cne 'true') {
        return $null
    }
    $commit = Invoke-CapturedProcess $git.Source ($gitArguments + @('rev-parse', 'HEAD')) $ModuleRoot $null $CommandEvidence
    $remote = Invoke-CapturedProcess $git.Source ($gitArguments + @('remote', 'get-url', 'origin')) $ModuleRoot $null $CommandEvidence -AllowFailure -OmitOutputFromEvidence
    $status = Invoke-CapturedProcess $git.Source ($gitArguments + @('status', '--short', '--untracked-files=all', '--', '.')) $ModuleRoot $null $CommandEvidence
    $changes = @($status.Stdout -split "`r?`n" | Where-Object { $_ })
    [ordered]@{
        commit = $commit.Stdout
        origin = if ($remote.ExitCode -eq 0) { Protect-GitRemoteUrl $remote.Stdout } else { $null }
        workingTreeDirty = $changes.Count -gt 0
        workingTreeChanges = $changes
    }
}

function Protect-GitRemoteUrl {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Url
    )

    $value = $Url.Trim()
    $uri = $null
    if ([Uri]::TryCreate($value, [UriKind]::Absolute, [ref]$uri) -and $uri.IsAbsoluteUri) {
        $builder = [UriBuilder]::new($uri)
        $builder.UserName = ''
        $builder.Password = ''
        $builder.Query = ''
        $builder.Fragment = ''
        return $builder.Uri.AbsoluteUri
    }

    $value = $value -replace '[?#].*$', ''
    $value -replace '^[^/\\@:]+@(?=[^/\\]+[:/])', ''
}

function Get-PhysicalPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $fullPath = [IO.Path]::GetFullPath($Path)
    $existingPath = $fullPath
    $missingSegments = [Collections.Generic.Stack[string]]::new()
    while (-not (Test-Path -LiteralPath $existingPath)) {
        $leaf = [IO.Path]::GetFileName($existingPath.TrimEnd([IO.Path]::DirectorySeparatorChar))
        Assert-Condition (-not [string]::IsNullOrEmpty($leaf)) "Cannot find an existing ancestor for path: $fullPath"
        $missingSegments.Push($leaf)
        $parent = [IO.Path]::GetDirectoryName($existingPath.TrimEnd([IO.Path]::DirectorySeparatorChar))
        Assert-Condition (-not [string]::IsNullOrEmpty($parent)) "Cannot find an existing ancestor for path: $fullPath"
        $existingPath = $parent
    }

    $resolvedPath = [PhysicalPath]::Resolve($existingPath)
    while ($missingSegments.Count -gt 0) {
        $resolvedPath = Join-Path $resolvedPath $missingSegments.Pop()
    }
    [IO.Path]::GetFullPath($resolvedPath)
}

function Assert-PhysicalPathOutsideRoot {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Candidate,

        [Parameter(Mandatory = $true)]
        [string]$Root,

        [Parameter(Mandatory = $true)]
        [string]$Description
    )

    $candidatePath = (Get-PhysicalPath $Candidate).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $rootPath = (Get-PhysicalPath $Root).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $rootPrefix = $rootPath + [IO.Path]::DirectorySeparatorChar
    $candidatePrefix = $candidatePath + [IO.Path]::DirectorySeparatorChar
    $pathsOverlap = $candidatePath.Equals($rootPath, [StringComparison]::OrdinalIgnoreCase) -or
        $candidatePath.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)
    $pathsOverlap = $pathsOverlap -or $rootPath.StartsWith($candidatePrefix, [StringComparison]::OrdinalIgnoreCase)
    Assert-Condition (-not $pathsOverlap) "$Description must not overlap protected root '$rootPath': $candidatePath"
}

function Assert-NoReparsePoints {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Root
    )

    $reparsePoint = Get-ChildItem -LiteralPath $Root -Force -Recurse |
        Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
        Select-Object -First 1
    if ($null -ne $reparsePoint) {
        throw "Refusing to clean a directory containing a reparse point: $($reparsePoint.FullName)"
    }
}

function Test-AvoidableMinGwRuntime {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Dependency
    )

    $normalized = $Dependency.ToLowerInvariant()
    $normalized -match '^libgcc_s_.+-1\.dll$' -or $normalized -in @(
        'libssp-0.dll'
        'libstdc++-6.dll'
        'libwinpthread-1.dll'
    )
}

function Initialize-CleanDirectory {
    param(
        [Parameter(Mandatory = $true)]
        [string]$BaseDirectory,

        [Parameter(Mandatory = $true)]
        [string]$LeafName,

        [string[]]$ProtectedRoots = @()
    )

    $basePath = [IO.Path]::GetFullPath($BaseDirectory)
    foreach ($protectedRoot in $ProtectedRoots) {
        Assert-PhysicalPathOutsideRoot $basePath $protectedRoot 'Work root'
    }
    New-Item -ItemType Directory -Path $basePath -Force | Out-Null
    $basePath = Get-PhysicalPath $basePath
    foreach ($protectedRoot in $ProtectedRoots) {
        Assert-PhysicalPathOutsideRoot $basePath $protectedRoot 'Work root'
    }

    $targetPath = [IO.Path]::GetFullPath((Join-Path $basePath $LeafName))
    $targetPhysicalPath = Get-PhysicalPath $targetPath
    $expectedPrefix = $basePath.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    Assert-Condition $targetPhysicalPath.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase) "Unsafe physical work directory path: $targetPhysicalPath"
    foreach ($protectedRoot in $ProtectedRoots) {
        Assert-PhysicalPathOutsideRoot $targetPhysicalPath $protectedRoot 'Work directory'
    }

    if (Test-Path -LiteralPath $targetPath) {
        $existing = Get-Item -LiteralPath $targetPath -Force
        Assert-Condition (($existing.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0) "Refusing to clean a reparse point: $targetPath"
        Assert-NoReparsePoints $targetPath
        Remove-Item -LiteralPath $targetPath -Recurse -Force
    }
    New-Item -ItemType Directory -Path $targetPath | Out-Null
    $createdPhysicalPath = Get-PhysicalPath $targetPath
    Assert-Condition $createdPhysicalPath.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase) "Created work directory escaped its physical root: $createdPhysicalPath"
    $createdPhysicalPath
}

function Invoke-SelfTest {
    $selfTestRoot = Initialize-CleanDirectory $WorkRoot 'self-test' @($repositoryRoot)
    try {
        $fixtureRoot = Join-Path $selfTestRoot 'module'
        $proxyRoot = Join-Path $selfTestRoot 'proxy'
        New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'nested') -Force | Out-Null
        $fixtureModulePath = 'github.com/Azure/ProxyFixture/windows/amd64'
        $fixtureVersion = 'v1.2.3-Preview.1'
        Write-Utf8File (Join-Path $fixtureRoot 'go.mod') "module $fixtureModulePath`n`ngo 1.23.0`n"
        Write-Utf8File (Join-Path $fixtureRoot 'fixture.go') "package driver`n"
        Write-Utf8File (Join-Path $fixtureRoot 'nested' 'fixture.txt') "fixture`n"

        $sourceFiles = @(Get-SourceFiles $fixtureRoot)
        $proxy = New-LocalGoProxy $fixtureRoot $sourceFiles $fixtureModulePath $fixtureVersion $proxyRoot
        Assert-Condition ($proxy.escapedModulePath -ceq 'github.com/!azure/!proxy!fixture/windows/amd64') 'Uppercase module path escaping failed.'
        Assert-Condition ($proxy.escapedVersion -ceq 'v1.2.3-!preview.1') 'Uppercase version escaping failed.'
        Assert-Condition (([IO.File]::ReadAllText($proxy.listPath)) -ceq "$fixtureVersion`n") '@v/list content is invalid.'
        Assert-Condition (([IO.File]::ReadAllText($proxy.modPath)) -ceq ([IO.File]::ReadAllText((Join-Path $fixtureRoot 'go.mod')))) '.mod does not match source go.mod.'
        $info = Get-Content -LiteralPath $proxy.infoPath -Raw | ConvertFrom-Json
        Assert-Condition ($info.Version -ceq $fixtureVersion) '.info contains the wrong version.'
        $expectedEntries = @(
            "$fixtureModulePath@$fixtureVersion/fixture.go"
            "$fixtureModulePath@$fixtureVersion/go.mod"
            "$fixtureModulePath@$fixtureVersion/nested/fixture.txt"
        )
        Assert-Condition (($proxy.zipEntries -join "`n") -ceq ($expectedEntries -join "`n")) 'Module ZIP entries do not have the expected prefix and forward-slash paths.'

        $protectedRoot = Join-Path $selfTestRoot 'protected'
        $junctionPath = Join-Path $selfTestRoot 'protected-alias'
        New-Item -ItemType Directory -Path $protectedRoot | Out-Null
        New-Item -ItemType Junction -Path $junctionPath -Target $protectedRoot | Out-Null
        $containmentRejected = $false
        try {
            Assert-PhysicalPathOutsideRoot (Join-Path $junctionPath 'nested') $protectedRoot 'Fixture work root'
        }
        catch {
            $containmentRejected = $true
        }
        Assert-Condition $containmentRejected 'Physical containment did not reject a junction alias into a protected root.'
        $ancestorRejected = $false
        try {
            Assert-PhysicalPathOutsideRoot $selfTestRoot $protectedRoot 'Fixture work root'
        }
        catch {
            $ancestorRejected = $true
        }
        Assert-Condition $ancestorRejected 'Physical containment did not reject an ancestor of a protected root.'

        Assert-Condition ((Protect-GitRemoteUrl 'https://user:secret@example.test/org/repo.git?token=secret#fragment') -ceq 'https://example.test/org/repo.git') 'HTTPS remote URL redaction failed.'
        Assert-Condition ((Protect-GitRemoteUrl 'git@example.test:org/repo.git?token=secret') -ceq 'example.test:org/repo.git') 'SSH remote URL redaction failed.'
        Assert-Condition ((Get-Sha256Text 'abc') -ceq 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad') 'SHA256 text hashing failed.'
        Assert-Condition ((Format-GoToolCommand 'C:\Program Files\gcc.exe') -ceq '"C:\Program Files\gcc.exe"') 'Go tool path quoting failed.'
        Assert-Condition (Test-AvoidableMinGwRuntime 'libgcc_s_sjlj-1.dll') 'SJLJ libgcc runtime detection failed.'
        Assert-Condition (Test-AvoidableMinGwRuntime 'libgcc_s_custom-1.dll') 'Generic libgcc runtime detection failed.'
        Assert-Condition (-not (Test-AvoidableMinGwRuntime 'kernel32.dll')) 'Windows system DLL was incorrectly classified as avoidable.'
        $separatorProxyUri = ConvertTo-FileProxyUri (Join-Path $selfTestRoot 'proxy,backup')
        Assert-Condition ($separatorProxyUri.Contains('%2C')) 'File proxy URI did not escape a Go proxy-list separator.'
        Assert-Condition ($separatorProxyUri -notmatch '[,|]') 'File proxy URI retained a Go proxy-list separator.'

        Write-Host 'Self-test passed: proxy metadata, ZIP structure, physical containment, URL redaction, and dependency checks are valid.'
    }
    finally {
        if (-not $KeepWorkDirectory -and (Test-Path -LiteralPath $selfTestRoot)) {
            Remove-Item -LiteralPath $selfTestRoot -Recurse -Force
        }
    }
}

if ($SelfTest) {
    Invoke-SelfTest
    return
}

Assert-CanonicalVersion $Version
$moduleRoot = (Resolve-Path -LiteralPath $ModuleDirectory).Path
$goModPath = Join-Path $moduleRoot 'go.mod'
Assert-Condition (Test-Path -LiteralPath $goModPath -PathType Leaf) "Module go.mod is missing: $goModPath"
$modulePath = Get-ModulePath $goModPath
$moduleGoVersion = Get-GoDirective $goModPath
Assert-Condition ($modulePath -ceq $expectedModulePath) "Expected module '$expectedModulePath', found '$modulePath'."

$workId = (Get-Sha256Text "$modulePath`n$Version`n$moduleRoot").Substring(0, 16)
$workDirectory = Initialize-CleanDirectory $WorkRoot "rehearsal-$workId" @($moduleRoot, $repositoryRoot)
$proxyRoot = Join-Path $workDirectory 'proxy'
$consumerRoot = Join-Path $workDirectory 'consumer'
$evidencePath = Join-Path $workDirectory 'evidence.json'
New-Item -ItemType Directory -Path $proxyRoot, $consumerRoot -Force | Out-Null

$commands = [Collections.Generic.List[object]]::new()
$evidence = [ordered]@{
    schemaVersion = 1
    status = 'running'
    startedUtc = (Get-Date).ToUniversalTime().ToString('o')
    module = [ordered]@{
        path = $modulePath
        version = $Version
        goVersion = $moduleGoVersion
        sourceDirectory = $moduleRoot
    }
    workDirectory = $workDirectory
    commands = $commands
}
$failure = $null

try {
    $sourceFiles = @(Get-SourceFiles $moduleRoot)
    $sourceTree = Get-ModuleTreeEvidence $moduleRoot $sourceFiles
    $evidence.module['sourceTree'] = $sourceTree
    $evidence.module['provenance'] = Get-OptionalProvenance $moduleRoot
    $evidence.module['git'] = Get-GitProvenance $moduleRoot $commands

    $proxy = New-LocalGoProxy $moduleRoot $sourceFiles $modulePath $Version $proxyRoot
    $evidence['proxy'] = $proxy
    $stableSourceFiles = @(Get-SourceFiles $moduleRoot)
    $stableSourceTree = Get-ModuleTreeEvidence $moduleRoot $stableSourceFiles
    Assert-Condition ($sourceTree.sha256 -ceq $stableSourceTree.sha256) 'Module source changed while the proxy ZIP was being constructed; rerun against a stable source directory.'

    $goPath = Resolve-Application $GoExecutable 'Go executable'
    $compilerPath = Resolve-Application $CCompiler 'C compiler'
    if ([string]::IsNullOrWhiteSpace($ObjectDumpExecutable)) {
        $ObjectDumpExecutable = Join-Path ([IO.Path]::GetDirectoryName($compilerPath)) 'objdump.exe'
    }
    $objectDumpPath = Resolve-Application $ObjectDumpExecutable 'MinGW objdump executable'

    $isolated = Get-IsolatedEnvironment $proxyRoot $consumerRoot $goPath $compilerPath $objectDumpPath
    $environment = $isolated.values
    foreach ($directory in @(
        $environment['GOMODCACHE']
        $environment['GOCACHE']
        $environment['GOPATH']
    )) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }

    $evidence['isolation'] = [ordered]@{
        path = $environment['PATH']
        pathDirectories = $isolated.pathDirectories
        cargoOnPath = $false
        rustcOnPath = $false
    }
    $wherePath = Join-Path $env:SystemRoot 'System32\where.exe'
    $cargoCheck = Invoke-CapturedProcess $wherePath @('cargo.exe') $consumerRoot $environment $commands -AllowFailure
    $rustcCheck = Invoke-CapturedProcess $wherePath @('rustc.exe') $consumerRoot $environment $commands -AllowFailure
    $evidence.isolation['cargoOnPath'] = $cargoCheck.ExitCode -eq 0
    $evidence.isolation['rustcOnPath'] = $rustcCheck.ExitCode -eq 0
    Assert-Condition ($cargoCheck.ExitCode -ne 0) "Cargo remains available on the sanitized PATH: $($cargoCheck.Stdout)"
    Assert-Condition ($rustcCheck.ExitCode -ne 0) "rustc remains available on the sanitized PATH: $($rustcCheck.Stdout)"

    $compilerTarget = Invoke-CapturedProcess $compilerPath @('-dumpmachine') $consumerRoot $environment $commands
    Assert-Condition ($compilerTarget.Stdout -match '^x86_64-.*mingw32$') "C compiler target is not Windows/amd64 MinGW: $($compilerTarget.Stdout)"
    $compilerVersion = Invoke-CapturedProcess $compilerPath @('--version') $consumerRoot $environment $commands
    $goVersion = Invoke-CapturedProcess $goPath @('version') $consumerRoot $environment $commands
    $evidence['toolchains'] = [ordered]@{
        goPath = $goPath
        goVersion = $goVersion.Stdout
        compilerPath = $compilerPath
        compilerTarget = $compilerTarget.Stdout
        compilerVersion = ($compilerVersion.Stdout -split "`n")[0]
        objectDumpPath = $objectDumpPath
    }

    $consumerGoMod = @"
module local.example/cosmos-driver-rehearsal

go $moduleGoVersion

require $modulePath $Version
"@
    $consumerSource = @"
package main

/*
const char *cosmos_version(void);
*/
import "C"

import (
	"fmt"
	_ "$modulePath"
)

func main() {
	version := C.cosmos_version()
	if version == nil {
		panic("cosmos_version returned null")
	}
	fmt.Println(C.GoString(version))
}
"@
    Write-Utf8File (Join-Path $consumerRoot 'go.mod') ($consumerGoMod.Replace("`r`n", "`n") + "`n")
    Write-Utf8File (Join-Path $consumerRoot 'main.go') ($consumerSource.Replace("`r`n", "`n") + "`n")
    $writtenGoMod = [IO.File]::ReadAllText((Join-Path $consumerRoot 'go.mod'))
    Assert-Condition ($writtenGoMod -notmatch '(?m)^\s*replace\s+') 'Consumer go.mod must not contain a replace directive.'
    $evidence['consumer'] = [ordered]@{
        directory = $consumerRoot
        goMod = $writtenGoMod
        usesReplaceDirective = $false
    }

    $goEnvNames = @(
        'GOOS'
        'GOARCH'
        'CGO_ENABLED'
        'CC'
        'GO111MODULE'
        'GOWORK'
        'GOPROXY'
        'GOSUMDB'
        'GOTOOLCHAIN'
        'GOMODCACHE'
        'GOCACHE'
        'GOPATH'
    )
    $goEnvironment = Invoke-CapturedProcess $goPath (@('env', '-json') + $goEnvNames) $consumerRoot $environment $commands
    $evidence['goEnvironment'] = $goEnvironment.Stdout | ConvertFrom-Json

    $download = Invoke-CapturedProcess $goPath @('mod', 'download', '-json', "$modulePath@$Version") $consumerRoot $environment $commands
    $evidence.consumer['download'] = $download.Stdout | ConvertFrom-Json
    $moduleList = Invoke-CapturedProcess $goPath @('list', '-m', 'all') $consumerRoot $environment $commands
    $evidence.consumer['resolvedModules'] = @($moduleList.Stdout -split "`r?`n" | Where-Object { $_ })
    Assert-Condition ($moduleList.Stdout -notmatch '=>') 'Resolved module list unexpectedly contains a replacement.'

    $executablePath = Join-Path $consumerRoot 'cosmos-driver-rehearsal.exe'
    [void](Invoke-CapturedProcess $goPath @('build', '-trimpath', '-o', $executablePath, '.') $consumerRoot $environment $commands)
    Assert-Condition (Test-Path -LiteralPath $executablePath -PathType Leaf) "Go build did not produce $executablePath."
    $versionResult = Invoke-CapturedProcess $executablePath @() $consumerRoot $environment $commands
    Assert-Condition (-not [string]::IsNullOrWhiteSpace($versionResult.Stdout)) 'cosmos_version produced no output.'

    $dependencyResult = Invoke-CapturedProcess $objectDumpPath @('-p', $executablePath) $consumerRoot $environment $commands -OmitOutputFromEvidence
    $dependencies = @(
        [regex]::Matches($dependencyResult.Stdout, '(?im)^\s*DLL Name:\s*(\S+)\s*$') |
            ForEach-Object { $_.Groups[1].Value } |
            Sort-Object -Unique
    )
    Assert-Condition ($dependencies.Count -gt 0) 'objdump did not report executable dependencies.'
    $avoidableDependencies = @(
        $dependencies | Where-Object { Test-AvoidableMinGwRuntime $_ }
    )
    $executable = Get-Item -LiteralPath $executablePath
    $evidence.consumer['versionOutput'] = $versionResult.Stdout
    $evidence.consumer['executable'] = [ordered]@{
        path = $executablePath
        sizeBytes = $executable.Length
        sha256 = Get-FileSha256 $executablePath
        dependencies = $dependencies
        avoidableMinGwRuntimeDependencies = $avoidableDependencies
    }
    Assert-Condition ($avoidableDependencies.Count -eq 0) "Executable depends on avoidable MinGW runtime DLLs: $($avoidableDependencies -join ', ')"

    $evidence.status = 'passed'
    Write-Host "Local Go proxy rehearsal passed for $modulePath@$Version."
    Write-Host "cosmos_version: $($versionResult.Stdout)"
    Write-Host "Executable size: $($executable.Length) bytes"
}
catch {
    $failure = $_
    $evidence.status = 'failed'
    $evidence['failure'] = [ordered]@{
        message = $_.Exception.Message
        category = [string]$_.CategoryInfo.Category
        scriptStackTrace = $_.ScriptStackTrace
    }
}
finally {
    $evidence['completedUtc'] = (Get-Date).ToUniversalTime().ToString('o')
    Write-Utf8File $evidencePath (($evidence | ConvertTo-Json -Depth 12) + "`n")
}

if ($null -ne $failure) {
    throw [InvalidOperationException]::new(
        "Local Go proxy rehearsal failed. Evidence retained at '$evidencePath'. $($failure.Exception.Message)",
        $failure.Exception
    )
}

if ($KeepWorkDirectory) {
    Write-Host "Evidence retained at: $evidencePath"
}
else {
    Assert-PhysicalPathOutsideRoot $workDirectory $moduleRoot 'Work directory'
    Assert-PhysicalPathOutsideRoot $workDirectory $repositoryRoot 'Work directory'
    Assert-NoReparsePoints $workDirectory
    Remove-Item -LiteralPath $workDirectory -Recurse -Force
    Write-Host 'Temporary rehearsal directory removed.'
}
