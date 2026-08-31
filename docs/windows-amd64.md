# Windows/amd64 native driver artifact

The `windows/amd64` Go module packages an optimized static build of
`azure_data_cosmos_driver_native` for `x86_64-pc-windows-gnu`. It is generated
from the Azure SDK for Rust source and blank-imported by a cgo consumer.

## Artifact identity

| Field | Value |
|---|---|
| Rust source commit | `6e174cab7e5a18bc89995dcd58fdda44c4e52dda` |
| Generator | `New-GoModules.ps1` from Azure SDK for Rust PR #4991, commit `658eb40066f05b1e82a24c079ccfe948ee905c2f` |
| Target | `windows/amd64` (`x86_64-pc-windows-gnu`) |
| Native interface | `azure_data_cosmos_driver_native` `0.1.0` |
| Rust driver | `azure_data_cosmos_driver` `0.7.0`, features `tokio` and `rustls` |
| Archive | `native/libazurecosmosdriver.a`, 20,498,556 bytes, SHA-256 `24f35ca88eb21915301e9ccd90fb9606220ca81eb6e6938449ca71bbca019894` |
| Header | `azurecosmosdriver.h`, 85,140 bytes, SHA-256 `6ffaed7ff60f3aa399db4e4de0c06880f7f0d848d94b62f55fb33b084d999e4f` |
| Header Git blob | `43ec1b7cd1955c3acce8b33b10d82c02b285661a` |

The generated root [`provenance.json`](../provenance.json) binds the source,
package versions, target, archive, and header. The schema-v3
[per-target metadata](../metadata/windows-amd64/rust-driver-native-interface-metadata.json)
records the build identity, primary toolchains, and rustc-reported native
libraries. [`SHA256SUMS`](../SHA256SUMS) is the generator-compatible archive
checksum file. The header is present at the module root and under `native/`,
matching the downstream layout enforced by PR #4991.

## Build

The artifact was built with:

- Rust `1.95.0` (`rustc 59807616e`, Cargo `f2d3ce0bd`)
- `cargo-auditable` `0.7.5`
- cbindgen `0.29.4`
- MSYS2 MinGW-w64 GCC `15.2.0`
- Go `1.25.12` for generator link validation

```powershell
$env:CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER = "gcc"
$env:CC_x86_64_pc_windows_gnu = "gcc"

cargo auditable build --release `
  -p azure_data_cosmos_driver_native `
  --target x86_64-pc-windows-gnu `
  --config 'profile.release.opt-level="z"' `
  --config 'profile.release.lto="fat"' `
  --config 'profile.release.codegen-units=1' `
  --config 'profile.release.strip="symbols"'
```

The default unwind panic strategy is retained because the native FFI boundary
uses panic catching.

## Linking

The module first links the driver archive, forces only `winpthread` static, then
restores normal selection for Windows system libraries:

```text
-L${SRCDIR}/native -lazurecosmosdriver
-Wl,-Bstatic -lwinpthread -Wl,-Bdynamic
-lws2_32 -luserenv -lntdll -lbcrypt -lncrypt -lsecur32 -lcrypt32
-ladvapi32 -lkernel32 -luser32 -lpsapi
```

At the pinned source, rustc reported `-lwindows.0.52.0`, but PR #4991 does not
copy that Cargo registry import library or its search path into the downstream
module. The generated link probe consequently fails on a clean consumer. This
bootstrap uses PR #4991's documented Windows fallback list, which links the
same exact-commit archive without a Cargo registry dependency. The upstream
generator must resolve this packaging gap before production publication can
replace this file safely.

## Validation

The repository smoke module under `tests/windows-amd64`:

1. blank-imports this module;
2. builds a cgo executable against the packaged header;
3. calls `cosmos_version()` and checks it against
   `AZURECOSMOSDRIVER_H_VERSION`; and
4. inspects the executable import table, rejecting non-system runtime DLLs
   such as `libwinpthread-1.dll`.

Run it on Windows with cgo and MinGW-w64:

```powershell
$env:CGO_ENABLED = "1"
$env:CC = "C:\msys64\mingw64\bin\gcc.exe"
go test ./...
```

Production publication is still blocked on merging and enabling the PR #4991
pipeline, fixing its missing `windows.0.52.0` packaging, and producing governed
1ES provenance/SBOM evidence. This repository does not fabricate the absent
`_manifest` output for this local bootstrap build.
