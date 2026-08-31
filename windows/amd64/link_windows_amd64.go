// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Derived from New-GoModules.ps1 in Azure/azure-sdk-for-rust PR #4991.
// Target: windows-amd64  triple: x86_64-pc-windows-gnu

//go:build cgo && windows && amd64

// The current rustc output includes -lwindows.0.52.0 without its Cargo-only
// search path. Use the generator matrix's self-contained Windows fallback
// libraries until the upstream generator packages that import library.
package driver

// #cgo LDFLAGS: -L${SRCDIR}/native -lazurecosmosdriver -Wl,-Bstatic -lwinpthread -Wl,-Bdynamic -lws2_32 -luserenv -lntdll -lbcrypt -lncrypt -lsecur32 -lcrypt32 -ladvapi32 -lkernel32 -luser32 -lpsapi
// #include "azurecosmosdriver.h"
import "C"
