# Azure Cosmos DB Native Driver

Prebuilt **native Azure Cosmos DB driver** static libraries, packaged as
per-platform [Go](https://go.dev/) modules so that
[`azure-sdk-for-go`](https://github.com/Azure/azure-sdk-for-go) (Cosmos DB v2,
over an FFI/cgo binding) can statically link the shared Rust driver.

This repository only **distributes the compiled artifacts**. The driver itself
is built from the
[`azure_data_cosmos_driver_native`](https://github.com/Azure/azure-sdk-for-rust/tree/main/sdk/cosmos/azure_data_cosmos_driver_native)
crate in [`azure-sdk-for-rust`](https://github.com/Azure/azure-sdk-for-rust) —
the C-ABI wrapper over `azure_data_cosmos_driver` — and the source of record
lives there.

## Status

> **This repository is under active bootstrap. Its contents and structure are
> still being defined.**

<!-- TODO: replace the placeholders below once the crew has agreed details. -->

- **Release state:** _TBD_ — currently a manually built drop for local
  development and testing.
- **Supported platforms:** _TBD_ — see the table below for what exists today.
- **Versioning / release tags:** _TBD_.
- **Publishing pipeline (signed, released module zips):** _TBD_.

## Repository layout

One Go module per `GOOS/GOARCH`, each shipping a prebuilt static library:

| Platform (`GOOS/GOARCH`) | Module | State |
| --- | --- | --- |
| `darwin/arm64` | [`darwin/arm64`](./darwin/arm64) | Available |
| `linux/amd64` | [`linux/amd64`](./linux/amd64) | Available |
| `linux/amd64` musl | [`linux/amd64-musl`](./linux/amd64-musl) | Available |
| `linux/arm64` | [`linux/arm64`](./linux/arm64) | Available |
| `linux/arm64` musl | [`linux/arm64-musl`](./linux/arm64-musl) | Available |

Each target module has no Go API surface — it is **blank-imported** by the
consuming package so that its `#cgo LDFLAGS` participate in the final program
link. See each module's own `README.md` for build and linking details.

## Pull request validation

Generated-driver pull requests are checked by
[`Generated driver validation`](./.github/workflows/generated-driver-validation.yml).
The validator discovers every nested Go module, verifies its generated link
file and module identity, and cryptographically cross-checks every archive and
header against both `provenance.json` and `SHA256SUMS`. On Linux AMD64 it also
links and runs a temporary cgo consumer that compares the runtime native-driver
version with the committed header version, first through normal module
replacement and then after `go mod vendor`. The vendored check verifies that Go
preserved the root-level archive and header byte-for-byte before linking them.
The repository's `.gitignore` explicitly preserves generated `arm64` target
directories that the inherited Visual Studio rules would otherwise omit.
Validation also rejects undeclared files under the generated platform roots and
prevents a pull request from deleting or removing targets already published on
its base branch.

The validator requires the vendoring-safe provenance schema 2 layout introduced
by
[`Azure/azure-sdk-for-rust#5289`](https://github.com/Azure/azure-sdk-for-rust/pull/5289).
Each module contains `libazurecosmosdriver.a` and `azurecosmosdriver.h` at its
root, links with `-L${SRCDIR}`, and records the exact archive path and Rust
toolchain in `provenance.json`. Legacy `native/` directories, `.syso` files, and
schema 1 manifests are rejected.

Run the checks locally with:

```text
go test ./eng/validate-generated-driver
go run ./eng/validate-generated-driver/main.go integrity
go run ./eng/validate-generated-driver/main.go native-smoke
```

## Third-party code

This repository does **not** vendor third-party source. It distributes a
**prebuilt static archive** (`libazurecosmosdriver.a`) that statically links
open-source Rust crates pulled in transitively by the driver, including
(non-exhaustive): `tokio`, `rustls`, `reqwest`, `h2`, `serde` / `serde_json`,
`futures`, `url`, `base64`, `bytes`, `uuid`, and `tracing`. These dependencies
are licensed under the MIT and/or Apache-2.0 licenses. The authoritative,
versioned dependency graph is defined by the driver crate in
[`azure-sdk-for-rust`](https://github.com/Azure/azure-sdk-for-rust); refer to
that repository for the complete dependency set and their licenses.

See [NOTICE.txt](./NOTICE.txt) for the third-party OSS notice. Each generated
drop also includes [`provenance.json`](./provenance.json),
[`SHA256SUMS`](./SHA256SUMS), and generated SBOM metadata under
[`_manifest`](./_manifest/) so consumers can verify the source commit,
dependency inventory, binary hashes, and Microsoft Rust toolchain identity used
for the distributed archives.

## Privacy and telemetry

This repository does not contain code that directly collects telemetry. The
native driver artifacts are linked into consuming SDKs and applications;
telemetry or diagnostics behavior is controlled by those consumers, not by this
distribution repository.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). This project has adopted the
[Microsoft Open Source Code of Conduct](https://opensource.microsoft.com/codeofconduct/).

## Reporting security issues and security bugs

Security issues and bugs should be reported privately, via email, to the
Microsoft Security Response Center (MSRC) at <secure@microsoft.com>. You should
receive a response within 24 hours. If for some reason you do not, please
follow up via email to ensure we received your original message. Further
information, including the MSRC PGP key, can be found in the
[Security TechCenter](https://www.microsoft.com/msrc/faqs-report-an-issue).
This repository also includes Microsoft repository security guidance in
[SECURITY.md](./SECURITY.md).

## Trademarks

This project may contain trademarks or logos for projects, products, or
services. Authorized use of Microsoft trademarks or logos is subject to and must
follow
[Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general).
Use of Microsoft trademarks or logos in modified versions of this project must
not cause confusion or imply Microsoft sponsorship. Any use of third-party
trademarks or logos are subject to those third-party's policies.
