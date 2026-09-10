# Azure Cosmos DB Native Driver

Prebuilt **native Azure Cosmos DB driver** static libraries, packaged as per-platform
[Go](https://go.dev/) modules so that [`azure-sdk-for-go`](https://github.com/Azure/azure-sdk-for-go)
(Cosmos DB v2, over an FFI/cgo binding) can statically link the shared Rust driver.

This repository only **distributes the compiled artifacts**. The driver itself is built from the
[`azure_data_cosmos_driver_native`](https://github.com/Azure/azure-sdk-for-rust/tree/main/sdk/cosmos/azure_data_cosmos_driver_native)
crate in [`azure-sdk-for-rust`](https://github.com/Azure/azure-sdk-for-rust) — the C-ABI wrapper over
`azure_data_cosmos_driver` — and the source of record lives there.

## Status

> **This repository is under active bootstrap. Its contents and structure are still being defined.**

- **Release state:** generated-driver pull requests can be validated and planned for release, but
  publishing is not automated.
- **Supported platforms:** _TBD_ — see the table below for what exists today.
- **Versioning / release tags:** six lockstep, path-prefixed module tags (for example,
  `linux/amd64/v0.1.0`).
- **Publishing pipeline (signed, released module zips):** _TBD_.

## Repository layout

One Go module per `GOOS/GOARCH`, each shipping a prebuilt static library:

| Platform (`GOOS/GOARCH`) | Module | State |
|---|---|---|
| `windows/amd64` | [`windows/amd64`](./windows/amd64) | Available |
| `darwin/arm64` | `darwin/arm64` | In progress |
| `linux/amd64`, `linux/arm64` | — | Not yet available |

Each target module has no Go API surface — it is **blank-imported** by the consuming package so that
its `#cgo LDFLAGS` participate in the final program link. See each module's own `README.md` for build
and linking details.

## Pull request validation

Generated-driver pull requests are checked by
[`Generated driver validation`](./.github/workflows/generated-driver-validation.yml). The validator
discovers every nested Go module, verifies its generated link file and module identity, and
cryptographically cross-checks every archive and header against both `provenance.json` and
`SHA256SUMS`. On Linux AMD64 it also links and runs a temporary cgo consumer that compares the
runtime native-driver version with the committed header version. This smoke test uses normal Go
module replacement semantics, matching module-cache/proxy consumption without vendoring. The
repository's `.gitignore`
explicitly preserves generated `arm64` target directories that the inherited Visual Studio rules
would otherwise omit. Validation also rejects undeclared files under the generated platform roots
and prevents a pull request from deleting or removing targets already published on its base branch.

Run the checks locally with:

```text
go test ./eng/validate-generated-driver/main.go ./eng/validate-generated-driver/main_test.go
go run ./eng/validate-generated-driver/main.go integrity
go run ./eng/validate-generated-driver/main.go native-smoke
```

## Read-only release planning

The
[`Plan generated driver release`](./.github/workflows/plan-generated-driver-release.yml)
manual workflow is Phase 1 of the release process. It accepts a merged generated-driver pull request
number and a canonical stable `X.Y.Z` version without a leading `v`. It must be dispatched from the
repository default branch so the planner and validator are built from trusted `main` content. The
workflow derives the durable merge commit from GitHub, requires the pull request to target `main`,
checks out that exact commit, verifies it is reachable from the current remote default branch, and runs
the generated-driver integrity validator against the pull request's base SHA.

The planner requires these six nested modules and proposes one lockstep tag for each:

| Module | Proposed tag shape |
|---|---|
| `windows/amd64` | `windows/amd64/vX.Y.Z` |
| `linux/amd64` | `linux/amd64/vX.Y.Z` |
| `linux/arm64` | `linux/arm64/vX.Y.Z` |
| `linux/amd64-musl` | `linux/amd64-musl/vX.Y.Z` |
| `linux/arm64-musl` | `linux/arm64-musl/vX.Y.Z` |
| `darwin/arm64` | `darwin/arm64/vX.Y.Z` |

The requested version must match the native-interface version in `provenance.json` and every generated
header. The independently versioned Rust driver version is reported in the plan but does not determine
the Go module version. Major versions 2 and later are rejected because the module paths do not
currently carry Go's required `/vN` suffix.

The plan status is:

- `eligible_to_publish` when all six proposed tags are absent.
- `already_published` when all six tags, including annotated tags, resolve to the derived merge
  commit.
- `blocked` for partial publication, a tag at another commit, malformed tag state, invalid release
  identity, or failed integrity/continuity checks.

The workflow publishes a GitHub Step Summary and a non-sensitive JSON plan artifact. It has only
`contents: read` and `pull-requests: read` permissions and **cannot publish, delete, or move tags or
create a GitHub Release**.

Because this is a private module repository, consumers must configure private-module authentication
and `GOPRIVATE=github.com/Azure/azure-cosmos-driver` (or an appropriate broader pattern). The planner
does not configure consumer credentials. Provenance and archive hashes are structurally and
cryptographically cross-checked by the existing validator, but Phase 1 does not claim independent
COSE signature verification; that remains a future repository-governance decision.

## Atomic release publication

The
[`Publish generated driver release`](./.github/workflows/publish-generated-driver-release.yml)
manual workflow is Phase 2. It accepts the same `pr_number` and canonical `version` inputs and first
runs the complete read-only Phase 1 plan. The publication job uses the `driver-release` environment,
where repository administrators can configure required reviewers and prevent self-review. The
workflow is serialized with the `generated-driver-release` concurrency group and does not cancel an
in-progress publication.

The workflow defaults to read-only permissions. Only the environment-gated publication job receives
`contents: write`; `pull-requests: read` is used to re-resolve the merged pull request. Both jobs build
the planner, publisher, and integrity validator from the immutable default-branch commit that
dispatched the workflow. The candidate merge commit is checked out only as data and no candidate
scripts are executed. The authenticated candidate checkout is limited to the publication job because
Git credentials are required for its single push.

After approval, the publisher re-fetches the default branch, re-resolves the pull request, and reruns
the integrity, release-identity, continuity, module, and remote-tag checks. The approved canonical
plan digest must exactly match the fresh plan, including the PR, merge SHA, version, default-branch
tip, and six module tags. If all tags are absent, it creates six deterministic, unsigned annotated
tags targeting the exact merge commit and invokes one command equivalent to:

```text
git push --atomic origin refs/tags/<module>/vX.Y.Z:refs/tags/<module>/vX.Y.Z ...
```

There is no sequential fallback. The publisher never forces, moves, or deletes a tag. If all six tags
already resolve recursively to the expected merge commit, the run is an `already_published` no-op.
Partial, mismatched, malformed, or drifted state is `blocked`. An ambiguous result after the one
atomic push attempt is `indeterminate`; rerun only after inspecting the result, and a fresh rerun can
then establish `already_published`. The workflow verifies all six remote refs after publication and
emits a non-sensitive JSON result and GitHub Step Summary.

The tags are currently unsigned because the repository has no established non-secret signing
mechanism; configuring signing is an administrator governance decision. Private-module download
verification also remains an integration gate until credentials or a private proxy can be configured
without exposing tokens. Phase 2 does not create an aggregate tag or GitHub Release.

## Third-party code

This repository does **not** vendor third-party source. It distributes a **prebuilt static archive**
(`libazurecosmosdriver.a`) that statically links open-source Rust crates pulled in transitively by the
driver, including (non-exhaustive): `tokio`, `rustls`, `reqwest`, `h2`, `serde` / `serde_json`,
`futures`, `url`, `base64`, `bytes`, `uuid`, and `tracing`. These dependencies are licensed under the
MIT and/or Apache-2.0 licenses. The authoritative, versioned dependency graph is defined by the driver
crate in [`azure-sdk-for-rust`](https://github.com/Azure/azure-sdk-for-rust); refer to that repository
for the complete dependency set and their licenses.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). This project has adopted the
[Microsoft Open Source Code of Conduct](https://opensource.microsoft.com/codeofconduct/).

## Trademarks

This project may contain trademarks or logos for projects, products, or services. Authorized use of
Microsoft trademarks or logos is subject to and must follow
[Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general).
Use of Microsoft trademarks or logos in modified versions of this project must not cause confusion or
imply Microsoft sponsorship. Any use of third-party trademarks or logos are subject to those
third-party's policies.
