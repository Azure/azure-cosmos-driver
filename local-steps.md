# Manual generated-driver tag publication

These steps publish the one-time `0.1.0` release from merged generated-driver pull request
`Azure/azure-cosmos-driver#15`. They intentionally create only the five path-prefixed Go module
tags declared by provenance schema 2:

```text
darwin/arm64/v0.1.0
linux/amd64/v0.1.0
linux/arm64/v0.1.0
linux/amd64-musl/v0.1.0
linux/arm64-musl/v0.1.0
```

Do not create `windows/amd64/v0.1.0`, an aggregate `v0.1.0` tag, or a GitHub Release.

The commands use PowerShell 7, Git, GitHub CLI, and Go 1.25. Run them from a clean, reviewed
checkout containing the release planner and validator in this repository. That checkout is the
trusted control plane; the PR merge is checked out separately and treated only as candidate data.

## 1. Set immutable release inputs

```powershell
$ErrorActionPreference = "Stop"
$Repository = "Azure/azure-cosmos-driver"
$PrNumber = 15
$Version = "0.1.0"
$DefaultBranch = "main"
$ExpectedUpstreamSource = "db4df3039eebd33bd2e587164e9a54e6f2ffb65b"
$ExpectedRustVersion = "0.8.0"
$ExpectedModules = @(
    "linux/amd64",
    "linux/arm64",
    "linux/amd64-musl",
    "linux/arm64-musl",
    "darwin/arm64"
)
$ExpectedTags = @($ExpectedModules | ForEach-Object { "$_/v$Version" })
```

Confirm the control checkout is clean and record the reviewed tooling commit:

```powershell
$ControlRoot = (Get-Location).Path
if (git status --porcelain) {
    throw "The trusted control checkout has uncommitted changes."
}
$ControlCommit = git rev-parse HEAD
Write-Host "Trusted release tooling commit: $ControlCommit"
```

## 2. Build the trusted tools

Create an isolated temporary workspace and build only from the control checkout:

```powershell
$ReleaseRoot = Join-Path ([System.IO.Path]::GetTempPath()) `
    "cosmos-driver-release-$([guid]::NewGuid())"
$CandidateRoot = Join-Path $ReleaseRoot "candidate"
$Planner = Join-Path $ReleaseRoot "release-planner.exe"
$Validator = Join-Path $ReleaseRoot "generated-driver-validator.exe"
$Metadata = Join-Path $ReleaseRoot "pr-metadata.json"
$PlanPath = Join-Path $ReleaseRoot "release-plan.json"
$SummaryPath = Join-Path $ReleaseRoot "release-summary.md"

New-Item -ItemType Directory -Path $ReleaseRoot | Out-Null

go build -o $Planner `
    (Join-Path $ControlRoot "eng\plan-generated-driver-release\main.go")
if ($LASTEXITCODE -ne 0) {
    throw "Failed to build the trusted release planner."
}

go build -o $Validator `
    (Join-Path $ControlRoot "eng\validate-generated-driver\main.go")
if ($LASTEXITCODE -ne 0) {
    throw "Failed to build the trusted generated-driver validator."
}
```

## 3. Resolve PR #15 through GitHub

Authenticate GitHub CLI without printing the token:

```powershell
gh auth status
$env:GH_TOKEN = gh auth token
if (-not $env:GH_TOKEN) {
    throw "GitHub CLI did not provide an authentication token."
}
$GitHubCredentialArguments = @(
    "-c", "credential.helper=",
    "-c", "credential.helper=!gh auth git-credential"
)
```

If any later command fails, remove `GH_TOKEN` from the current shell before investigating.

Resolve the durable merge commit. The command fails unless PR #15 exists in this repository, is
merged into `main`, and has a full merge commit SHA:

```powershell
& $Planner resolve-pr `
    -repository $Repository `
    -pr-number $PrNumber `
    -default-branch $DefaultBranch `
    -version $Version `
    -output $Metadata `
    -plan-output $PlanPath `
    -summary $SummaryPath

if ($LASTEXITCODE -ne 0) {
    throw "PR #15 is not a valid merged release candidate."
}

$PrMetadata = Get-Content $Metadata -Raw | ConvertFrom-Json
$MergeSha = $PrMetadata.merge_commit_sha
Write-Host "Derived merge commit: $MergeSha"
```

Never replace `$MergeSha` with the PR head SHA or a manually selected commit.

## 4. Check out the exact merge commit as candidate data

```powershell
git @GitHubCredentialArguments clone --no-checkout `
    "https://github.com/$Repository.git" `
    $CandidateRoot
git @GitHubCredentialArguments -C $CandidateRoot fetch --prune origin $DefaultBranch --tags
git -C $CandidateRoot checkout --detach $MergeSha

git -C $CandidateRoot cat-file -e "$MergeSha^{commit}"
if ($LASTEXITCODE -ne 0) {
    throw "The derived merge SHA is not a commit."
}

git -C $CandidateRoot merge-base --is-ancestor $MergeSha "origin/$DefaultBranch"
if ($LASTEXITCODE -ne 0) {
    throw "The PR merge is not reachable from current origin/main."
}
```

Do not execute scripts from the candidate checkout.

## 5. Generate and review the read-only release plan

```powershell
& $Planner plan `
    -root $CandidateRoot `
    -repository $Repository `
    -version $Version `
    -pr-metadata $Metadata `
    -validator $Validator `
    -output $PlanPath `
    -summary $SummaryPath

if ($LASTEXITCODE -ne 0) {
    throw "Release planning failed. Do not create tags."
}

$Plan = Get-Content $PlanPath -Raw | ConvertFrom-Json
if ($Plan.status -eq "already_published") {
    Write-Host "All five tags are already published at the merge commit. No mutation is required."
    return
}
if ($Plan.status -ne "eligible_to_publish") {
    throw "Plan status is '$($Plan.status)'; do not publish."
}
if ($Plan.upstream_source_sha -ne $ExpectedUpstreamSource) {
    throw "Unexpected upstream Rust source commit."
}
if ($Plan.native_interface_version -ne $Version) {
    throw "Native interface version does not match the requested version."
}
if ($Plan.rust_driver_version -ne $ExpectedRustVersion) {
    throw "Unexpected Rust driver version."
}

$ActualModules = @($Plan.modules.path | Sort-Object)
$SortedExpectedModules = @($ExpectedModules | Sort-Object)
if (Compare-Object $SortedExpectedModules $ActualModules) {
    throw "The release plan does not contain exactly the expected five modules."
}
if (Compare-Object @($ExpectedTags | Sort-Object) @($Plan.modules.tag | Sort-Object)) {
    throw "The release plan does not contain exactly the expected five tags."
}

Get-Content $SummaryPath
```

Review and retain `release-plan.json` and `release-summary.md` before continuing.

## 6. Revalidate immediately before mutation

Re-fetch `main` and tags, resolve the PR again, and rebuild the plan. This closes the approval-to-push
window for PR, branch, candidate, and tag drift.

```powershell
$FinalMetadata = Join-Path $ReleaseRoot "final-pr-metadata.json"
$FinalPlanPath = Join-Path $ReleaseRoot "final-release-plan.json"
$FinalSummaryPath = Join-Path $ReleaseRoot "final-release-summary.md"

git @GitHubCredentialArguments -C $CandidateRoot fetch --prune origin $DefaultBranch --tags

& $Planner resolve-pr `
    -repository $Repository `
    -pr-number $PrNumber `
    -default-branch $DefaultBranch `
    -version $Version `
    -output $FinalMetadata `
    -plan-output $FinalPlanPath `
    -summary $FinalSummaryPath
if ($LASTEXITCODE -ne 0) {
    throw "Final PR resolution failed."
}

$FinalPrMetadata = Get-Content $FinalMetadata -Raw | ConvertFrom-Json
if ($FinalPrMetadata.merge_commit_sha -ne $MergeSha) {
    throw "The derived PR merge SHA changed."
}

& $Planner plan `
    -root $CandidateRoot `
    -repository $Repository `
    -version $Version `
    -pr-metadata $FinalMetadata `
    -validator $Validator `
    -output $FinalPlanPath `
    -summary $FinalSummaryPath
if ($LASTEXITCODE -ne 0) {
    throw "Final release revalidation failed."
}

$FinalPlan = Get-Content $FinalPlanPath -Raw | ConvertFrom-Json
if ($FinalPlan.status -ne "eligible_to_publish") {
    throw "Final plan is '$($FinalPlan.status)'; no tags may be created."
}
if ($FinalPlan.source_pr -ne $PrNumber -or
    $FinalPlan.derived_merge_sha -ne $MergeSha -or
    $FinalPlan.requested_version -ne $Version -or
    $FinalPlan.upstream_source_sha -ne $ExpectedUpstreamSource -or
    $FinalPlan.native_interface_version -ne $Version -or
    $FinalPlan.rust_driver_version -ne $ExpectedRustVersion) {
    throw "The immutable release identity changed after review."
}
if (Compare-Object $SortedExpectedModules @($FinalPlan.modules.path | Sort-Object)) {
    throw "The module set changed after review."
}
if (Compare-Object @($ExpectedTags | Sort-Object) @($FinalPlan.modules.tag | Sort-Object)) {
    throw "The tag set changed after review."
}
```

## 7. Create five annotated tags locally

The final plan has just confirmed that all five remote tags are absent. Also block any local
collision:

```powershell
$Tags = @($FinalPlan.modules.tag)
foreach ($Tag in $Tags) {
    git -C $CandidateRoot show-ref --verify --quiet "refs/tags/$Tag"
    if ($LASTEXITCODE -eq 0) {
        throw "Local tag $Tag already exists; stop and investigate."
    }
}
```

Create deterministic unsigned annotated tags at the exact merge commit:

```powershell
foreach ($Module in $FinalPlan.modules) {
    $Message = @"
Azure Cosmos DB generated driver module release

Module: $($Module.path)
Version: $Version
Driver merge SHA: $MergeSha
Upstream source SHA: $($FinalPlan.upstream_source_sha)
Native interface version: $($FinalPlan.native_interface_version)
Rust implementation version: $($FinalPlan.rust_driver_version)
"@

    git -C $CandidateRoot tag `
        --annotate `
        --no-sign `
        --message $Message `
        $Module.tag `
        $MergeSha
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to create local tag $($Module.tag). No remote push has occurred."
    }
}

foreach ($Tag in $Tags) {
    $Resolved = git -C $CandidateRoot rev-parse "$Tag^{}"
    if ($Resolved -ne $MergeSha) {
        throw "Local tag $Tag does not peel to the release merge commit."
    }
}
```

## 8. Publish all five tags in one atomic push

This is the only remote mutation:

```powershell
$PushArguments = @("push", "--atomic", "origin")
$PushArguments += @(
    $Tags | ForEach-Object {
        "refs/tags/${_}:refs/tags/${_}"
    }
)

git @GitHubCredentialArguments -C $CandidateRoot @PushArguments
if ($LASTEXITCODE -ne 0) {
    throw "Atomic push failed. Do not retry sequentially, force, move, or delete any tag."
}
```

There is deliberately no sequential fallback.

## 9. Verify every remote tag

```powershell
foreach ($Tag in $Tags) {
    $Lines = @(
        git @GitHubCredentialArguments -C $CandidateRoot ls-remote --tags origin `
            "refs/tags/$Tag" `
            "refs/tags/$Tag^{}"
    )
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to read remote tag $Tag."
    }

    $Peeled = @($Lines | Where-Object { $_ -match '\^\{\}$' })
    if ($Peeled.Count -ne 1) {
        throw "Remote tag $Tag is missing or is not an annotated tag."
    }

    $RemoteCommit = ($Peeled[0] -split '\s+')[0]
    if ($RemoteCommit -ne $MergeSha) {
        throw "Remote tag $Tag resolves to $RemoteCommit instead of $MergeSha."
    }
    Write-Host "Verified $Tag -> $RemoteCommit"
}
```

Retain the two plan JSON files, summaries, merge SHA, control commit, command output, operator
identity, approver identity, and UTC publication time. Do not include `GH_TOKEN` or other
credentials in the evidence.

## 10. Remove temporary credentials and files

After evidence is stored in an approved location:

```powershell
Remove-Item Env:\GH_TOKEN -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $ReleaseRoot -Recurse -Force
```

If any step fails, stop. Never repair a partial or disputed release by forcing, deleting, moving, or
reusing a tag.
