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

<!-- TODO: replace the placeholders below once the crew has agreed the details. -->

- **Release state:** _TBD_ — currently a manually built drop for local development and testing.
- **Supported platforms:** _TBD_ — see the table below for what exists today.
- **Versioning / release tags:** _TBD_.
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
would otherwise omit.

Run the checks locally with:

```text
go test ./eng/validate-generated-driver/main.go ./eng/validate-generated-driver/main_test.go
go run ./eng/validate-generated-driver/main.go integrity
go run ./eng/validate-generated-driver/main.go native-smoke
```

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
