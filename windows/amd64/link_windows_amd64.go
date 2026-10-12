//go:build cgo && windows && amd64

// Package driver carries the prebuilt native Cosmos driver static library for
// windows/amd64 and the link flags that pull it into the final cgo link. It has
// no Go API surface: it is blank-imported by the public cosmos package so that
// its #cgo LDFLAGS participate in the program link.
package driver

/*
#cgo LDFLAGS: -L${SRCDIR}/native -lazurecosmosdriver
#cgo LDFLAGS: -lws2_32 -luserenv -lntdll -lbcrypt -lncrypt -lsecur32 -lcrypt32
#cgo LDFLAGS: -ladvapi32 -lkernel32 -luser32 -lpsapi
*/
import "C"
