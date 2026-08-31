//go:build cgo && windows && amd64

package main

/*
#cgo CFLAGS: -I${SRCDIR}/../../windows/amd64
#include "azurecosmosdriver.h"

static const char *header_version(void) {
	return AZURECOSMOSDRIVER_H_VERSION;
}
*/
import "C"

import (
	"fmt"
	"os"

	_ "github.com/Azure/azure-cosmos-driver/windows/amd64"
)

func main() {
	headerVersion := C.GoString(C.header_version())
	libraryVersion := C.GoString(C.cosmos_version())
	if headerVersion == "" || libraryVersion != headerVersion {
		fmt.Fprintf(os.Stderr, "header version %q does not match library version %q\n", headerVersion, libraryVersion)
		os.Exit(1)
	}

	fmt.Println(libraryVersion)
}
