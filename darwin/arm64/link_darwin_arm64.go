//go:build cgo && darwin && arm64

// Package driver carries the prebuilt native Cosmos driver shared library for
// darwin/arm64 and the link flags that pull it into the final cgo link. It has
// no Go API surface: it is blank-imported by the public cosmos package so that
// its #cgo LDFLAGS participate in the program link.
//
// The library is linked dynamically, so libazurecosmosdriver.dylib must be
// resolvable at run time, not just at build time. dyld searches LC_RPATH
// entries in order, so the executable-relative ones are listed first: a dylib
// shipped alongside the binary must win over the build machine's module cache.
// The module cache path is kept last so that `go run` and `go test` work
// without staging a copy, but it is only a fallback.
//
// Building any package that imports this one therefore requires:
//
//	CGO_LDFLAGS_ALLOW='^-Wl,-rpath,@(executable_path|loader_path)$' go build ./...
//
// cgo's default LDFLAGS allowlist rejects rpath values beginning with '@'
// (see cmd/go/internal/work/security.go), so without that the build fails with
// "invalid flag in #cgo LDFLAGS". Dropping the '@' entries instead would let
// the build succeed but leave the binary dependent on the builder's module
// cache path, which fails at startup on any other machine.
package driver

/*
#cgo LDFLAGS: -L${SRCDIR}/native -lazurecosmosdriver
#cgo LDFLAGS: -Wl,-rpath,@executable_path -Wl,-rpath,@loader_path -Wl,-rpath,${SRCDIR}/native
*/
import "C"
