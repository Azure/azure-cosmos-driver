# Generated driver release governance

Publication is operationally disabled in
[`eng/release-governance.json`](../eng/release-governance.json) until repository owners complete and
verify every control in this document. The readiness check uses only read-only GitHub APIs and fails
closed: `unknown_due_to_permissions` is never treated as ready.

## Current read-only audit

The Phase 3 audit confirmed:

- the repository is private and its default branch is `main`;
- `.github/CODEOWNERS` assigns the repository to `@Azure/azure-cosmos-sdk`;
- the environment-list API was readable and returned no environments, so `driver-release` is absent;
- the repository-ruleset APIs were readable and returned no tag rulesets;
- the visible active organization ruleset protects the default branch, including deletion and
  non-fast-forward restrictions, pull-request review, code-owner review, and last-push approval.

The Actions-permissions and classic branch-protection endpoints returned `404`. Those results are
permission/API ambiguous and are not evidence that the corresponding settings are absent. This audit
did not mutate repository state.

## Required environment

Create the `driver-release` environment before dispatching the publication workflow:

1. Require the `@Azure/azure-cosmos-sdk` team as the only reviewer. A repository owner must confirm
   that this is the intended release-approver team before setting
   `owner_confirmed_reviewer_team` to `true`.
2. Enable **Prevent self-review**.
3. Disable administrator bypass. If the UI/API does not expose this setting to the audit identity,
   capture an administrator screenshot or exported setting and set `admin_bypass_disabled` to `true`
   only after review. The current public environment REST response schema does not expose this toggle,
   so readiness deliberately requires this checked-in attestation instead of assuming a missing field
   means bypass is disabled.
4. Select **Selected branches and tags**, add exactly the branch policy `main`, and do not use the
   broader "protected branches" option.

The workflow already serializes releases with the `generated-driver-release` concurrency group and
does not cancel an in-progress run. The environment is an approval boundary, not a credential store
for personal tokens.

The break-glass procedure is to pause publication, record an incident/change reference, obtain
approval from a repository administrator who is not the release initiator, temporarily change the
minimum required setting, perform the smallest necessary recovery, restore the setting, and capture
before/after evidence. Existing tags must never be moved or deleted as part of break-glass recovery.

## Required tag rulesets

GitHub ruleset ref-name conditions use `fnmatch` with pathname semantics. In particular, `*` does not
match `/`. Ruleset API conditions use full ref names, so use these exact include patterns with no
exclude patterns:

```text
refs/tags/windows/amd64/v*
refs/tags/linux/amd64/v*
refs/tags/linux/arm64/v*
refs/tags/linux/amd64-musl/v*
refs/tags/linux/arm64-musl/v*
refs/tags/darwin/arm64/v*
```

For example, `refs/tags/linux/amd64/v0.1.0` matches its exact prefix.
`refs/tags/linux/arm64/v0.1.0`, `refs/tags/linux/amd64/extra/v0.1.0`, and
`refs/tags/v0.1.0` do not match the `linux/amd64` pattern. This behavior follows GitHub's
[ruleset condition syntax](https://docs.github.com/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/about-rulesets#using-fnmatch-syntax)
and the
[repository rules REST schema](https://docs.github.com/rest/repos/rules#get-a-repository-ruleset).

Use two active repository-scoped tag-target rulesets so permission to create a release cannot bypass
tag immutability:

| Ruleset | Rules | Bypass |
|---|---|---|
| `generated-driver-tag-creation` | Restrict creations | Exactly the approved release GitHub App integration, `always` mode |
| `generated-driver-tag-immutability` | Restrict updates, restrict deletions, block non-fast-forward updates | None |

Both rulesets must use exactly the six patterns above. Do not grant organization administrators,
repository administrators, teams, users, Deploy Keys, or the generic GitHub Actions integration a
bypass on the immutability ruleset. Never force, move, delete, or reuse a published version tag.

These rules intentionally do not protect or authorize an aggregate `release/v*` or `v*` tag. The
current process creates neither an aggregate tag nor a GitHub Release.

## Release identity

Do not broadly approve the generic GitHub Actions App as a ruleset bypass actor. That could authorize
other workflows using `GITHUB_TOKEN` to create release tags. The preferred actor is a GitHub App:

- install it only on `Azure/azure-cosmos-driver`;
- grant repository **Contents: write** and **Metadata: read**;
- grant **Pull requests: read** only if the publication workflow continues to resolve PR metadata
  with the App token;
- mint a one-hour installation token only after environment approval;
- store the App ID, installation ID, and private key using environment/repository secret handling;
- never commit a private key, installation token, or personal access token.

GitHub rulesets identify this actor as an `Integration` bypass actor. Record its numeric integration
ID in `eng/release-governance.json`, change `mode` to `github_app`, and update the workflow to mint and
use the installation token before enabling publication. Until that credential change is reviewed,
`publication_enabled` must remain `false`. The current workflow's environment-gated `GITHUB_TOKEN`
is not an approved release identity.

The release tags are unsigned annotated tags because no non-secret signing mechanism is established.
Tag signing is a future governance decision, not a prerequisite that should be simulated with a
committed key.

## Readiness and activation

Run the GET-only readiness check from trusted default-branch code:

```console
go run ./eng/plan-generated-driver-release/main.go governance \
  -repository Azure/azure-cosmos-driver \
  -config eng/release-governance.json \
  -output generated-driver-governance-readiness.json
```

It emits:

- `ready` only when checked-in activation/evidence is complete and all environment, branch-policy,
  reviewer, ruleset, rule, pattern, and actor checks exactly match;
- `not_ready` for a confirmed missing or mismatched control;
- `unknown_due_to_permissions` when an API request is inaccessible or cannot be interpreted.

The command paginates environment, deployment-branch-policy, and tag-ruleset reads. It issues only
`GET` requests. The publication workflow runs it in the pre-approval read-only job and cannot schedule
the environment-gated write job unless the result is `ready`. This prevents a dispatch from
implicitly creating and using an unprotected missing environment. The environment-gated job repeats
the same GET-only check immediately after approval and before its credential-bearing candidate
checkout, so governance weakened during the approval wait blocks before mutation.
Both checks emit machine-readable readiness JSON into the corresponding retained workflow artifact.

Use this activation sequence:

1. Merge the workflow and readiness code while `publication_enabled` remains `false`.
2. An administrator creates the environment and both rulesets and selects the dedicated App actor.
3. Capture owner/admin evidence, configure the App identity, update the workflow credential, record
   the integration ID and attestations, and enable publication through reviewed code.
4. Run the readiness check and retain its JSON plus environment/ruleset API exports or screenshots.
5. Run the Phase 1 planner against the merged generated-driver PR.
6. Obtain explicit human approval for the first publication.

Before enabling, capture the environment name, reviewer team, prevent-self-review setting, deployment
branch policy, admin-bypass setting, both ruleset IDs/names/enforcement/target/conditions/rules/bypass
actors, App installation identity and permission list, readiness JSON, approving change, and evidence
timestamp. Redact secrets and tokens.

## Version and consumption policy

The six module tags are immutable and published in lockstep. A bad release is corrected with a new
SemVer version; an existing tag is never repointed. A future policy may use Go's `retract` directive,
but generated-module retraction and upstream Rust automation are outside this phase.

Because the repository is private, consumers need `GOPRIVATE` and private Git authentication or a
private proxy. The release workflow cannot currently perform a secure external `go mod download`
verification without additional credential/proxy governance. This limitation must remain explicit
rather than exposing the release token to a consumer test.
