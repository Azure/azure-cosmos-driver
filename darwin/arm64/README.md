# azure-cosmos-driver — darwin/arm64

Prebuilt **native Cosmos DB driver** static library for macOS on Apple Silicon,
packaged as a Go module so `azure-sdk-for-go` (Cosmos V2 FFI) can statically link
it via cgo.

> **Status.** This is a manually-built drop for local development and testing.
> The static library is committed directly for now; a later publishing pipeline
> will replace this with signed, released module zips consumed via version tags.

See the repository root `README.md` for the cross-platform overview. This file
covers only what is specific to `darwin/arm64`.

## Layout

```
darwin/arm64/
  go.mod                        module github.com/Azure/azure-cosmos-driver/darwin/arm64
  link_darwin_arm64.go          build tags + #cgo LDFLAGS (pulls the .a into the link)
  native/libazurecosmosdriver.a prebuilt static library (from azure_data_cosmos_driver_native)
```

This module has no Go API surface — it is **blank-imported** by the consuming
package so that its `#cgo LDFLAGS` participate in the final program link:

```go
import _ "github.com/Azure/azure-cosmos-driver/darwin/arm64"
```

## Requirements

- Go 1.25+ with `CGO_ENABLED=1`
- Apple command-line tools (`xcode-select --install`) for clang and the macOS
  SDK / system frameworks
- Platform: `darwin/arm64`

## Source of the library

Built from `azure-sdk-for-rust` crate
[`sdk/cosmos/azure_data_cosmos_driver_native`](https://github.com/Azure/azure-sdk-for-rust/tree/main/sdk/cosmos/azure_data_cosmos_driver_native)
(the C-ABI wrapper over `azure_data_cosmos_driver`).

```bash
MACOSX_DEPLOYMENT_TARGET=11.0 \
RUSTFLAGS="--remap-path-prefix=$HOME/.cargo=/cargo --remap-path-prefix=$HOME/.rustup=/rustup --remap-path-prefix=$PWD=/src" \
  cargo build -p azure_data_cosmos_driver_native --release --target aarch64-apple-darwin
# -> target/aarch64-apple-darwin/release/libazurecosmosdriver.a
```

Neither environment variable is cosmetic:

- **`MACOSX_DEPLOYMENT_TARGET`** pins the archive's objects to a deployment
  target at or below the SDK the Go toolchain links against. Without it they are
  stamped with the host SDK version and every consumer build emits hundreds of
  `ld: warning: object file ... was built for newer 'macOS' version` lines.
- **`--remap-path-prefix`** keeps the builder's local paths out of the artifact.
  Rust panic-location strings live in `__TEXT,__cstring`, not DWARF, so they
  survive stripping and would otherwise be linked into every downstream binary.

Free-standing ABI currently exported (no runtime required):
`const char *cosmos_version(void);` / header macro `AZURECOSMOSDRIVER_H_VERSION`.

## Link flags

`cargo rustc -- --print native-static-libs` reports:

```
-framework Security -framework CoreFoundation -liconv -lSystem -lc -lm
```

`link_darwin_arm64.go` carries all of these except `-lSystem`, which the Go
darwin toolchain already links — repeating it makes `ld` warn about a duplicate
library.

## How a consumer links it (local, pre-pipeline)

Go cannot depend on a PR URL. A consumer wires this module in via a local
checkout using `go.work use` or a `replace` directive, e.g.:

```
replace github.com/Azure/azure-cosmos-driver/darwin/arm64 => <local checkout>/darwin/arm64
```

## Verification

A cgo consumer blank-imports this module, statically links the checked-in
`libazurecosmosdriver.a`, and calls `cosmos_version()` across the C ABI. It
builds **and runs** natively on Apple Silicon, with no linker warnings:

```
library version: 0.1.0
header  version: 0.1.0
PASS: Go consumer statically linked the REAL azurecosmosdriver and called cosmos_version() across the C ABI
```

`cosmos_version()` matches `AZURECOSMOSDRIVER_H_VERSION`, so there is no header
drift against the crate.

## Not yet here

- Release tags (`darwin/arm64/vX.Y.Z`) so consumers can `require` a version
  instead of a local checkout.
- `darwin/amd64` (Intel Macs).
- CI / publishing pipeline; these artifacts are not production-signed.
