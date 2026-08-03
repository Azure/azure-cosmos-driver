# azure-cosmos-driver

Prebuilt **native Cosmos DB driver** static libraries, packaged as per-platform Go
modules so `azure-sdk-for-go` (Cosmos V2 FFI) can statically link them via cgo.

> **Status.** This is a manually-built drop for local development and testing.
> The static library is committed directly for now; a later publishing pipeline
> will replace this with signed, released module zips consumed via version tags.

## Layout (one module per `GOOS/GOARCH`)

```
windows/amd64/
  go.mod                        module github.com/Azure/azure-cosmos-driver/windows/amd64
  link_windows_amd64.go         build tags + #cgo LDFLAGS (pulls the .a into the link)
  native/libazurecosmosdriver.a prebuilt static library (from azure_data_cosmos_driver_native)
```

Each target module has no Go API surface — it is **blank-imported** by the
consuming package so that its `#cgo LDFLAGS` participate in the final program
link.

## Source of the library

Built from `azure-sdk-for-rust` crate
[`sdk/cosmos/azure_data_cosmos_driver_native`](https://github.com/Azure/azure-sdk-for-rust/tree/main/sdk/cosmos/azure_data_cosmos_driver_native)
(the C-ABI wrapper over `azure_data_cosmos_driver`).

```
# windows/amd64 (GNU target so mingw cgo can link the archive)
cargo build -p azure_data_cosmos_driver_native --release --target x86_64-pc-windows-gnu
# -> target/x86_64-pc-windows-gnu/release/libazurecosmosdriver.a
```

Free-standing ABI currently exported (no runtime required):
`const char *cosmos_version(void);` / header macro `AZURECOSMOSDRIVER_H_VERSION`.

## How a consumer links it (local, pre-pipeline)

Go cannot depend on a PR URL. A consumer wires this module in via a local
checkout using `go.work use` or a `replace` directive, e.g.:

```
replace github.com/Azure/azure-cosmos-driver/windows/amd64 => <local checkout>/windows/amd64
```

Verified: a cgo consumer statically links `libazurecosmosdriver.a` and calls
`cosmos_version()` across the C ABI — both from this checked-in `.a` and from a
proxy-served module zip.

## Not yet here

- `darwin/arm64` (must be built on a Mac).
- Release tags (`windows/amd64/vX.Y.Z`) so consumers can `require` a version
  instead of a local checkout.
- CI / publishing pipeline.
