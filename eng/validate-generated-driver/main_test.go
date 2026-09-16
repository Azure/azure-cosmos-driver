// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateIntegrity(t *testing.T) {
	root := writeFixture(t)
	if err := validateIntegrity(root, io.Discard); err != nil {
		t.Fatalf("validateIntegrity() error = %v", err)
	}
}

func TestValidateIntegrityGeneratedTargetMatrix(t *testing.T) {
	root := writeMatrixFixture(t)
	if err := validateIntegrity(root, io.Discard); err != nil {
		t.Fatalf("validateIntegrity() error = %v", err)
	}
}

func TestValidateIntegrityRejectsLegacySchema(t *testing.T) {
	root := writeMatrixFixture(t)
	manifest := readTestProvenance(t, root)
	manifest.SchemaVersion = 1
	writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)

	err := validateIntegrity(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsupported provenance schema_version 1") {
		t.Fatalf("validateIntegrity() error = %v, want schema 1 rejection", err)
	}
}

func TestValidateIntegrityRejectsInvalidFlatLayout(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "missing static library path",
			mutate: func(t *testing.T, root string) {
				manifest := readTestProvenance(t, root)
				manifest.Targets[0].StaticLibraryPath = ""
				writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
			},
			want: "static_library_path is",
		},
		{
			name: "wrong static library path",
			mutate: func(t *testing.T, root string) {
				manifest := readTestProvenance(t, root)
				manifest.Targets[0].StaticLibraryPath = "linux/amd64/native/libazurecosmosdriver.a"
				writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
			},
			want: "static_library_path is",
		},
		{
			name: "unsafe static library path",
			mutate: func(t *testing.T, root string) {
				manifest := readTestProvenance(t, root)
				manifest.Targets[0].StaticLibraryPath = "../libazurecosmosdriver.a"
				writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
			},
			want: "static_library_path is",
		},
		{
			name: "archive retained under native",
			mutate: func(t *testing.T, root string) {
				moduleDirectory := filepath.Join(root, "linux", "amd64")
				if err := os.MkdirAll(filepath.Join(moduleDirectory, "native"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(
					filepath.Join(moduleDirectory, "libazurecosmosdriver.a"),
					filepath.Join(moduleDirectory, "native", "libazurecosmosdriver.a"),
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "static library",
		},
		{
			name: "stale native directory",
			mutate: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "linux", "amd64", "native"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "removed native directory",
		},
		{
			name: "stale syso output",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "linux", "amd64", "driver.syso"), "stale")
			},
			want: "unexpected file under generated platform roots",
		},
		{
			name: "legacy linker search path",
			mutate: func(t *testing.T, root string) {
				filename := filepath.Join(root, "linux", "amd64", "link_linux_amd64.go")
				contents, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(
					t,
					filename,
					strings.ReplaceAll(string(contents), "-L${SRCDIR}", "-L${SRCDIR}/native"),
				)
			},
			want: "references the removed native directory",
		},
		{
			name: "root archive tampered",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "linux", "amd64", "libazurecosmosdriver.a"), "tampered")
			},
			want: "SHA256 mismatch",
		},
		{
			name: "root header tampered",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "linux", "amd64", "azurecosmosdriver.h"), "tampered")
			},
			want: "root header SHA256 mismatch",
		},
		{
			name: "root checksum hash disagrees with provenance",
			mutate: func(t *testing.T, root string) {
				manifest := readTestProvenance(t, root)
				var checksums strings.Builder
				for _, target := range manifest.Targets {
					hash := target.StaticLibrarySHA256
					if target.ModulePath == "linux/amd64" {
						hash = strings.Repeat("0", 64)
					}
					fmt.Fprintf(&checksums, "%s  %s\n", hash, target.StaticLibraryPath)
				}
				writeTestFile(t, filepath.Join(root, "SHA256SUMS"), checksums.String())
			},
			want: "SHA256SUMS and provenance.json disagree",
		},
		{
			name: "root checksum path uses legacy layout",
			mutate: func(t *testing.T, root string) {
				manifest := readTestProvenance(t, root)
				var checksums strings.Builder
				for _, target := range manifest.Targets {
					checksumPath := target.StaticLibraryPath
					if target.ModulePath == "linux/amd64" {
						checksumPath = "linux/amd64/native/libazurecosmosdriver.a"
					}
					fmt.Fprintf(&checksums, "%s  %s\n", target.StaticLibrarySHA256, checksumPath)
				}
				writeTestFile(t, filepath.Join(root, "SHA256SUMS"), checksums.String())
			},
			want: "undeclared archive",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := writeMatrixFixture(t)
			test.mutate(t, root)
			err := validateIntegrity(root, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateIntegrity() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateIntegrityAgainstBasePreservesSchema2Continuity(t *testing.T) {
	root := writeMatrixFixture(t)
	commitFixture(t, root)
	writeMatrixFixtureAt(t, root)

	if err := validateIntegrityAgainstBase(root, "HEAD", io.Discard); err != nil {
		t.Fatalf("validateIntegrityAgainstBase() error = %v", err)
	}
}

func TestValidateIntegrityRejectsTamperedArchive(t *testing.T) {
	root := writeFixture(t)
	writeTestFile(t, filepath.Join(root, "linux", "amd64", "libazurecosmosdriver.a"), "tampered")
	err := validateIntegrity(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("validateIntegrity() error = %v, want SHA256 mismatch", err)
	}
}

func TestValidateIntegrityRejectsDuplicateModule(t *testing.T) {
	root := writeFixture(t)
	manifest := readTestProvenance(t, root)
	duplicate := manifest.Targets[0]
	duplicate.ID = "other-id"
	duplicate.Triple = "other-triple"
	manifest.Targets = append(manifest.Targets, duplicate)
	writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
	err := validateIntegrity(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "duplicate module path") {
		t.Fatalf("validateIntegrity() error = %v, want duplicate module path", err)
	}
}

func TestValidateIntegrityRejectsUnsafeModulePath(t *testing.T) {
	root := writeFixture(t)
	manifest := readTestProvenance(t, root)
	manifest.Targets[0].ModulePath = "../outside"
	writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
	err := validateIntegrity(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsafe module_path") {
		t.Fatalf("validateIntegrity() error = %v, want unsafe module_path", err)
	}
}

func TestValidateIntegrityRejectsUndeclaredNestedModule(t *testing.T) {
	root := writeFixture(t)
	writeTestFile(t, filepath.Join(root, "darwin", "arm64", "go.mod"), "module github.com/Azure/azure-cosmos-driver/darwin/arm64\n\ngo 1.25.0\n")
	err := validateIntegrity(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing from provenance.json") {
		t.Fatalf("validateIntegrity() error = %v, want missing provenance target", err)
	}
}

func TestValidateIntegrityRejectsUnexpectedGeneratedFile(t *testing.T) {
	root := writeFixture(t)
	writeTestFile(t, filepath.Join(root, "linux", "amd64", "README.md"), "unexpected")
	err := validateIntegrity(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unexpected file under generated platform roots") {
		t.Fatalf("validateIntegrity() error = %v, want unexpected generated file", err)
	}
}

func TestValidateIntegrityAgainstBaseRejectsReleaseDeletion(t *testing.T) {
	root := writeFixture(t)
	commitFixture(t, root)
	if err := validateIntegrityAgainstBase(root, "HEAD", io.Discard); err != nil {
		t.Fatalf("validateIntegrityAgainstBase() before deletion error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "linux")); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"provenance.json", "SHA256SUMS"} {
		if err := os.Remove(filepath.Join(root, filename)); err != nil {
			t.Fatal(err)
		}
	}
	err := validateIntegrityAgainstBase(root, "HEAD", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "generated release cannot be deleted") {
		t.Fatalf("validateIntegrityAgainstBase() error = %v, want release deletion failure", err)
	}
}

func TestValidateIntegrityAgainstBaseRejectsPublishedTargetRemoval(t *testing.T) {
	root := writeMatrixFixture(t)
	commitFixture(t, root)
	base := readTestProvenance(t, root)
	removed := base.Targets[len(base.Targets)-1]
	current := base
	current.Targets = append([]provenanceTarget(nil), base.Targets[:len(base.Targets)-1]...)
	writeTestJSON(t, filepath.Join(root, "provenance.json"), current)
	if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(removed.ModulePath))); err != nil {
		t.Fatal(err)
	}
	var checksums strings.Builder
	for _, target := range current.Targets {
		fmt.Fprintf(&checksums, "%s  %s\n", target.StaticLibrarySHA256, target.StaticLibraryPath)
	}
	writeTestFile(t, filepath.Join(root, "SHA256SUMS"), checksums.String())

	err := validateIntegrityAgainstBase(root, "HEAD", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "previously published target") {
		t.Fatalf("validateIntegrityAgainstBase() error = %v, want published target removal failure", err)
	}
}

func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	moduleDirectory := filepath.Join(root, "linux", "amd64")
	header := "#define AZURECOSMOSDRIVER_H_VERSION \"0.1.0\"\nconst char *cosmos_version(void);\n"
	archive := "representative-static-archive"
	writeTestFile(t, filepath.Join(moduleDirectory, "go.mod"), "module github.com/Azure/azure-cosmos-driver/linux/amd64\n\ngo 1.25.0\n")
	writeTestFile(t, filepath.Join(moduleDirectory, "azurecosmosdriver.h"), header)
	writeTestFile(t, filepath.Join(moduleDirectory, "libazurecosmosdriver.a"), archive)
	writeTestFile(t, filepath.Join(moduleDirectory, "link_linux_amd64.go"), `// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Code generated by New-GoModules.ps1; DO NOT EDIT.
// Target: linux-amd64-glibc  triple: x86_64-unknown-linux-gnu

//go:build cgo && linux && amd64

package driver

// #cgo LDFLAGS: -L${SRCDIR} -lazurecosmosdriver -lgcc_s -lutil -lrt -lpthread -lm -ldl -lc
// #include "azurecosmosdriver.h"
import "C"
`)

	target := provenanceTarget{
		ID:                  "linux-amd64-glibc",
		Triple:              "x86_64-unknown-linux-gnu",
		ModulePath:          "linux/amd64",
		StaticLibraryPath:   "linux/amd64/libazurecosmosdriver.a",
		StaticLibrarySHA256: testHash(archive),
		HeaderSHA256:        testHash(header),
	}
	manifest := provenance{
		SchemaVersion:          2,
		SourceCommit:           "85c4e1e01ee0b4c2dfdf9533dcad192b626af06d",
		NativeInterfaceCrate:   "azure_data_cosmos_driver_native",
		NativeInterfaceVersion: "0.1.0",
		RustDriverCrate:        "azure_data_cosmos_driver",
		RustDriverVersion:      "0.1.0",
		Targets:                []provenanceTarget{target},
	}
	writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
	writeTestFile(t, filepath.Join(root, "SHA256SUMS"), target.StaticLibrarySHA256+"  linux/amd64/libazurecosmosdriver.a\n")
	return root
}

func writeMatrixFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeMatrixFixtureAt(t, root)
	return root
}

func writeMatrixFixtureAt(t *testing.T, root string) {
	t.Helper()
	specs := []struct {
		id         string
		triple     string
		modulePath string
	}{
		{"windows-amd64", "x86_64-pc-windows-gnu", "windows/amd64"},
		{"linux-amd64-glibc", "x86_64-unknown-linux-gnu", "linux/amd64"},
		{"linux-arm64-glibc", "aarch64-unknown-linux-gnu", "linux/arm64"},
		{"linux-amd64-musl", "x86_64-unknown-linux-musl", "linux/amd64-musl"},
		{"linux-arm64-musl", "aarch64-unknown-linux-musl", "linux/arm64-musl"},
		{"darwin-arm64", "aarch64-apple-darwin", "darwin/arm64"},
	}
	for _, platform := range []string{"windows", "linux", "darwin"} {
		if err := os.RemoveAll(filepath.Join(root, platform)); err != nil {
			t.Fatal(err)
		}
	}
	header := "#define AZURECOSMOSDRIVER_H_VERSION \"0.1.0\"\nconst char *cosmos_version(void);\n"
	var targets []provenanceTarget
	var checksums strings.Builder
	for _, spec := range specs {
		parts := strings.Split(spec.modulePath, "/")
		goarch, _, _ := strings.Cut(parts[1], "-")
		archive := "representative-static-archive-" + spec.id
		moduleDirectory := filepath.Join(root, filepath.FromSlash(spec.modulePath))
		writeTestFile(t, filepath.Join(moduleDirectory, "go.mod"), fmt.Sprintf("module github.com/Azure/azure-cosmos-driver/%s\n\ngo 1.25.0\n", spec.modulePath))
		writeTestFile(t, filepath.Join(moduleDirectory, "azurecosmosdriver.h"), header)
		archivePath := filepath.Join(moduleDirectory, "libazurecosmosdriver.a")
		linkSearchPath := "-L${SRCDIR}"
		staticLibraryPath := filepath.ToSlash(filepath.Join(spec.modulePath, "libazurecosmosdriver.a"))
		writeTestFile(t, archivePath, archive)
		writeTestFile(t, filepath.Join(moduleDirectory, fmt.Sprintf("link_%s_%s.go", parts[0], goarch)), fmt.Sprintf(`// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Code generated by New-GoModules.ps1; DO NOT EDIT.
// Target: %s  triple: %s

//go:build cgo && %s && %s

package driver

// #cgo LDFLAGS: %s -lazurecosmosdriver
// #include "azurecosmosdriver.h"
import "C"
`, spec.id, spec.triple, parts[0], goarch, linkSearchPath))

		archiveHash := testHash(archive)
		targets = append(targets, provenanceTarget{
			ID:                  spec.id,
			Triple:              spec.triple,
			ModulePath:          spec.modulePath,
			StaticLibraryPath:   staticLibraryPath,
			StaticLibrarySHA256: archiveHash,
			HeaderSHA256:        testHash(header),
		})
		fmt.Fprintf(&checksums, "%s  %s\n", archiveHash, staticLibraryPath)
	}
	writeTestJSON(t, filepath.Join(root, "provenance.json"), provenance{
		SchemaVersion:          2,
		SourceCommit:           "85c4e1e01ee0b4c2dfdf9533dcad192b626af06d",
		NativeInterfaceCrate:   "azure_data_cosmos_driver_native",
		NativeInterfaceVersion: "0.1.0",
		RustDriverCrate:        "azure_data_cosmos_driver",
		RustDriverVersion:      "0.1.0",
		Targets:                targets,
	})
	writeTestFile(t, filepath.Join(root, "SHA256SUMS"), checksums.String())
}

func readTestProvenance(t *testing.T, root string) provenance {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest provenance
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeTestJSON(t *testing.T, filename string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filename, string(contents)+"\n")
}

func writeTestFile(t *testing.T, filename, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitFixture(t *testing.T, root string) {
	t.Helper()
	for _, arguments := range [][]string{
		{"init", "--quiet"},
		{"add", "."},
		{"-c", "user.name=Validator Test", "-c", "user.email=validator@example.invalid", "commit", "--quiet", "-m", "fixture"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}
}

func testHash(contents string) string {
	sum := sha256.Sum256([]byte(contents))
	return hex.EncodeToString(sum[:])
}
