# azure-cosmos-driver — darwin/arm64

Prebuilt **native Cosmos DB driver** shared library for macOS on Apple Silicon,
packaged as a Go module so `azure-sdk-for-go` (Cosmos V2 FFI) can link it via
cgo.

> **Status.** This is a manually-built drop for local development and testing.
> The shared library is committed directly for now; a later publishing pipeline
> will replace this with signed, released module zips consumed via version tags.

See the repository root `README.md` for the cross-platform overview. This file
covers only what is specific to `darwin/arm64`.

## Layout

```
darwin/arm64/
  go.mod                            module github.com/Azure/azure-cosmos-driver/darwin/arm64
  link_darwin_arm64.go              build tags + #cgo LDFLAGS (pulls the dylib into the link)
  native/libazurecosmosdriver.dylib prebuilt shared library (from azure_data_cosmos_driver_native)
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
- **`CGO_LDFLAGS_ALLOW` must be set** — see below

## This library is linked dynamically

The driver is a `.dylib`, not a `.a`. It is **not** embedded in the consuming
binary: it must be present and resolvable at run time. That has two consequences
every consumer needs to know about.

### 1. `CGO_LDFLAGS_ALLOW` is mandatory

```bash
CGO_LDFLAGS_ALLOW='^-Wl,-rpath,@(executable_path|loader_path)$' go build ./...
```

Use that exact anchored pattern rather than a broad one like `-Wl,-rpath,@.*`.
cgo matches the pattern against the whole flag (`allow.FindString(arg) == arg`)
and takes the allowed branch *before* consulting its deny list, and clang splits
`-Wl,a,b,c` into separate `ld` arguments — so a `.*` that matches commas lets
arbitrary linker flags through for **every** cgo package in the build, not just
this one. Prefer passing it per-invocation over exporting it into your shell
profile.

`link_darwin_arm64.go` records three rpath entries, in this order:

1. `@executable_path` and `@loader_path` — a dylib shipped next to the binary.
2. this module's `native/` directory — so `go run` and `go test` work straight
   from the module cache.

dyld searches `LC_RPATH` entries in order, so the executable-relative entries
come first deliberately: on the machine that built the binary the module cache
path also resolves, and if it were listed first it would shadow the dylib you
actually shipped.

cgo's default LDFLAGS allowlist rejects rpath values that begin with `@`
(`cmd/go/internal/work/security.go`), so without that variable the build fails:

```
invalid flag in #cgo LDFLAGS: -Wl,-rpath,@executable_path
```

This is a build-time error by design. Dropping the `@` entries would make the
build succeed while leaving the binary pinned to the builder's module cache
path — which then fails at startup on any other machine.

### 2. The dylib must ship with your binary

`go build` does not copy it. Place `libazurecosmosdriver.dylib` next to the
executable (or anywhere on its rpath / `DYLD_LIBRARY_PATH`) when you distribute:

```
myapp
libazurecosmosdriver.dylib
```

Otherwise the program aborts before `main` runs:

```
dyld: Library not loaded: @rpath/libazurecosmosdriver.dylib
Abort trap: 6
```

Note this also means a binary built today stops working if the Go module cache
is later cleared (`go clean -modcache`), unless the dylib travels with it.

The dylib carries `current version 0.0.0` and no compatibility version, so dyld
will happily load a mismatched copy placed next to a binary rather than refusing
it. Until the crate starts versioning the dylib, keep the dylib and the binary
that was built against it together.

### 3. `go mod vendor` does not carry the library

`go mod vendor` only copies files from a module's *package* directories. The
library lives in `native/`, which contains no Go files, so it is silently
dropped and a vendored build fails:

```
ld: library 'azurecosmosdriver' not found
```

(The same applies to the C header in the `cosmos-core` wrapper module, which
lives in `include/`.) Vendored consumers must copy `native/` into the vendor
tree themselves, or avoid `-mod=vendor` for now. This is not specific to dynamic
linking — a committed `.a` is dropped the same way.

## Source of the library

Built from `azure-sdk-for-rust` crate
[`sdk/cosmos/azure_data_cosmos_driver_native`](https://github.com/Azure/azure-sdk-for-rust/tree/main/sdk/cosmos/azure_data_cosmos_driver_native)
(the C-ABI wrapper over `azure_data_cosmos_driver`).

```bash
MACOSX_DEPLOYMENT_TARGET=11.0 \
RUSTFLAGS="--remap-path-prefix=$HOME/.cargo=/cargo --remap-path-prefix=$HOME/.rustup=/rustup --remap-path-prefix=$PWD=/src -C link-arg=-Wl,-install_name,@rpath/libazurecosmosdriver.dylib" \
  cargo build -p azure_data_cosmos_driver_native --release --target aarch64-apple-darwin
# -> target/aarch64-apple-darwin/release/libazurecosmosdriver.dylib
```

None of those settings are cosmetic:

- **`-C link-arg=-Wl,-install_name,@rpath/...`** sets the dylib's install name.
  Rust otherwise records the **absolute path of the build directory**, which is
  copied into every binary that links it — leaking the builder's home directory
  and pinning consumers to a path that exists only on the build machine.
- **`MACOSX_DEPLOYMENT_TARGET`** pins the objects to a deployment target at or
  below the SDK the Go toolchain links against. Without it they are stamped with
  the host SDK version and consumer builds emit hundreds of
  `ld: warning: object file ... was built for newer 'macOS' version` lines.
- **`--remap-path-prefix`** keeps the builder's local paths out of the artifact.
  Rust panic-location strings live in `__TEXT,__cstring`, not DWARF, so they
  survive stripping.

Verify the install name before publishing a rebuilt dylib:

```bash
otool -D native/libazurecosmosdriver.dylib   # expect: @rpath/libazurecosmosdriver.dylib
```

The dylib exports 54 `cosmos_*` symbols (completion queues, account refs,
operation handles, …). The one used to smoke-test linkage is the free-standing
`const char *cosmos_version(void);`, paired with the header macro
`AZURECOSMOSDRIVER_H_VERSION`.

Note that dynamic linking makes all of those symbols dynamically visible and
interposable, which static linking did not.

## Link flags

The dylib records its own system dependencies (`Security`, `CoreFoundation`,
`libiconv`, `libSystem`), so unlike a static archive the consumer does not
re-declare them. `link_darwin_arm64.go` carries only the library itself and the
rpath entries.

## How a consumer links it (local, pre-pipeline)

Go cannot depend on a PR URL. A consumer wires this module in via a local
checkout using `go.work use` or a `replace` directive, e.g.:

```
replace github.com/Azure/azure-cosmos-driver/darwin/arm64 => <local checkout>/darwin/arm64
```

## Verification

A cgo consumer blank-imports this module, dynamically links the checked-in
`libazurecosmosdriver.dylib`, and calls `cosmos_version()` across the C ABI. It
builds **and runs** natively on Apple Silicon, with no linker warnings:

```
library version: 0.1.0
header  version: 0.1.0
PASS: Go consumer dynamically linked the REAL azurecosmosdriver and called cosmos_version() across the C ABI
```

`cosmos_version()` matches `AZURECOSMOSDRIVER_H_VERSION`, so there is no header
drift against the crate. Also verified: the resulting binary still runs after
being copied away from the build tree with the dylib alongside it, and fails
cleanly at startup when the dylib is absent.

## Not yet here

- Release tags (`darwin/arm64/vX.Y.Z`) so consumers can `require` a version
  instead of a local checkout.
- `darwin/amd64` (Intel Macs).
- Code signing / notarization. The committed dylib is ad-hoc (`linker-signed`)
  with no team identifier, so a consumer that re-signs with hardened runtime and
  library validation must re-sign the dylib under the same team ID or dyld will
  reject it at load. These artifacts are not production-signed.
- CI / publishing pipeline.
