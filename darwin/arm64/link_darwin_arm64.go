//go:build cgo && darwin && arm64

// Package driver carries the prebuilt native Cosmos driver static library for
// darwin/arm64 and the link flags that pull it into the final cgo link. It has
// no Go API surface: it is blank-imported by the public cosmos package so that
// its #cgo LDFLAGS participate in the program link.
package driver

/*
#cgo LDFLAGS: -L${SRCDIR}/native -lazurecosmosdriver
#cgo LDFLAGS: -framework Security -framework CoreFoundation -liconv -lc -lm

// rustc reports the full set as:
//   -framework Security -framework CoreFoundation -liconv -lSystem -lc -lm
// -lSystem is omitted above because the Go toolchain already links it on
// darwin, and repeating it makes ld emit a duplicate-library warning.
*/
import "C"
