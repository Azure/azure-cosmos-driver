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
	root := writeFixture(t)
	manifest := readTestProvenance(t, root)
	manifest.SchemaVersion = 1
	writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)

	err := validateIntegrity(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsupported provenance schema_version 1") {
		t.Fatalf("validateIntegrity() error = %v, want schema 1 rejection", err)
	}
}

func TestValidateIntegrityRejectsInvalidSchemaTwoLayout(t *testing.T) {
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
			name: "legacy static library path",
			mutate: func(t *testing.T, root string) {
				manifest := readTestProvenance(t, root)
				manifest.Targets[0].StaticLibraryPath = "linux/amd64/native/libazurecosmosdriver.a"
				writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
			},
			want: "static_library_path is",
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
				writeTestFile(t, filename, strings.ReplaceAll(string(contents), "-L${SRCDIR}", "-L${SRCDIR}/native"))
			},
			want: "unexpected source-relative library search path",
		},
		{
			name: "incorrect linker search path",
			mutate: func(t *testing.T, root string) {
				filename := filepath.Join(root, "linux", "amd64", "link_linux_amd64.go")
				contents, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filename, strings.ReplaceAll(string(contents), "-L${SRCDIR}", "-L${SRCDIR}/bogus"))
			},
			want: "unexpected source-relative library search path",
		},
		{
			name: "target toolchain mismatch",
			mutate: func(t *testing.T, root string) {
				manifest := readTestProvenance(t, root)
				manifest.Targets[0].Toolchain.Target = "aarch64-unknown-linux-gnu"
				writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
			},
			want: "does not match target triple",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := writeFixture(t)
			test.mutate(t, root)
			err := validateIntegrity(root, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateIntegrity() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateIntegrityAgainstBaseAllowsSchemaOneToSchemaTwoMigration(t *testing.T) {
	root := writeMatrixFixture(t)
	current := readTestProvenance(t, root)
	contents, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(contents, &base); err != nil {
		t.Fatal(err)
	}
	base["schema_version"] = float64(1)
	delete(base, "rust_toolchain")
	for _, target := range base["targets"].([]any) {
		target := target.(map[string]any)
		delete(target, "static_library_path")
		delete(target, "toolchain")
	}
	writeTestJSON(t, filepath.Join(root, "provenance.json"), base)
	commitFixture(t, root)
	writeTestJSON(t, filepath.Join(root, "provenance.json"), current)

	if err := validateIntegrityAgainstBase(root, "HEAD", io.Discard); err != nil {
		t.Fatalf("validateIntegrityAgainstBase() migration error = %v", err)
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

func TestValidateVendoredModule(t *testing.T) {
	workDirectory := t.TempDir()
	moduleDirectory := t.TempDir()
	moduleImport := moduleRoot + "/linux/amd64"
	for _, file := range []struct {
		name     string
		contents string
	}{
		{"azurecosmosdriver.h", "header"},
		{"libazurecosmosdriver.a", "archive"},
	} {
		writeTestFile(t, filepath.Join(moduleDirectory, file.name), file.contents)
		writeTestFile(
			t,
			filepath.Join(workDirectory, "vendor", filepath.FromSlash(moduleImport), file.name),
			file.contents,
		)
	}

	if err := validateVendoredModule(workDirectory, moduleImport, moduleDirectory); err != nil {
		t.Fatalf("validateVendoredModule() error = %v", err)
	}

	writeTestFile(
		t,
		filepath.Join(workDirectory, "vendor", filepath.FromSlash(moduleImport), "libazurecosmosdriver.a"),
		"tampered",
	)
	err := validateVendoredModule(workDirectory, moduleImport, moduleDirectory)
	if err == nil || !strings.Contains(err.Error(), "differs from the generated module") {
		t.Fatalf("validateVendoredModule() error = %v, want hash mismatch", err)
	}
}

func TestGoModVendorPreservesNativeFiles(t *testing.T) {
	root := writeFixture(t)
	moduleDirectory := filepath.Join(root, "linux", "amd64")
	moduleImport := moduleRoot + "/linux/amd64"
	workDirectory := t.TempDir()
	writeTestFile(
		t,
		filepath.Join(workDirectory, "go.mod"),
		fmt.Sprintf(
			"module cosmos-driver-vendor-test\n\ngo 1.25.0\n\nrequire %s v0.0.0\n\nreplace %s => %s\n",
			moduleImport,
			moduleImport,
			filepath.ToSlash(moduleDirectory),
		),
	)
	writeTestFile(
		t,
		filepath.Join(workDirectory, "main.go"),
		fmt.Sprintf("package main\n\nimport _ %q\n\nfunc main() {}\n", moduleImport),
	)

	command := exec.Command("go", "mod", "vendor")
	command.Dir = workDirectory
	command.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS=linux", "GOARCH=amd64")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go mod vendor failed: %v\n%s", err, output)
	}
	if err := validateVendoredModule(workDirectory, moduleImport, moduleDirectory); err != nil {
		t.Fatalf("validateVendoredModule() after go mod vendor error = %v", err)
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
		Toolchain:           testTargetToolchain("x86_64-unknown-linux-gnu"),
	}
	manifest := provenance{
		SchemaVersion:          2,
		SourceCommit:           "85c4e1e01ee0b4c2dfdf9533dcad192b626af06d",
		NativeInterfaceCrate:   "azure_data_cosmos_driver_native",
		NativeInterfaceVersion: "0.1.0",
		RustDriverCrate:        "azure_data_cosmos_driver",
		RustDriverVersion:      "0.1.0",
		RustToolchain:          testProvenanceToolchain(),
		Targets:                []provenanceTarget{target},
	}
	writeTestJSON(t, filepath.Join(root, "provenance.json"), manifest)
	writeTestFile(t, filepath.Join(root, "SHA256SUMS"), target.StaticLibrarySHA256+"  linux/amd64/libazurecosmosdriver.a\n")
	return root
}

func writeMatrixFixture(t *testing.T) string {
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
	root := t.TempDir()
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
		writeTestFile(t, filepath.Join(moduleDirectory, "libazurecosmosdriver.a"), archive)
		writeTestFile(t, filepath.Join(moduleDirectory, fmt.Sprintf("link_%s_%s.go", parts[0], goarch)), fmt.Sprintf(`// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Code generated by New-GoModules.ps1; DO NOT EDIT.
// Target: %s  triple: %s

//go:build cgo && %s && %s

package driver

// #cgo LDFLAGS: -L${SRCDIR} -lazurecosmosdriver
// #include "azurecosmosdriver.h"
import "C"
`, spec.id, spec.triple, parts[0], goarch))

		archiveHash := testHash(archive)
		staticLibraryPath := filepath.ToSlash(filepath.Join(spec.modulePath, "libazurecosmosdriver.a"))
		targets = append(targets, provenanceTarget{
			ID:                  spec.id,
			Triple:              spec.triple,
			ModulePath:          spec.modulePath,
			StaticLibraryPath:   staticLibraryPath,
			StaticLibrarySHA256: archiveHash,
			HeaderSHA256:        testHash(header),
			Toolchain:           testTargetToolchain(spec.triple),
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
		RustToolchain:          testProvenanceToolchain(),
		Targets:                targets,
	})
	writeTestFile(t, filepath.Join(root, "SHA256SUMS"), checksums.String())
	return root
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

func testProvenanceToolchain() provenanceToolchain {
	return provenanceToolchain{
		Provider:                "microsoft",
		Manager:                 "msrustup",
		ManagerVersion:          "msrustup 5.7.1-20260901.1",
		Channel:                 "ms-prod-1.95",
		InstallerPackageVersion: "1.95.0-ms-20260618.5",
		RustcRelease:            "1.95.0",
		RustcCommitHash:         "ed80dadd6a554c8b82a54f4b9a22ec7b36b514a7",
		CargoVersion:            "cargo 1.95.0 (1.95.0-ms-20260618.5+ed80dadd6a)",
	}
}

func testTargetToolchain(triple string) targetToolchain {
	return targetToolchain{
		SelectedToolchain:        "ms-prod-1.95",
		Sysroot:                  "/toolchain",
		RustcExecutable:          "/toolchain/bin/rustc",
		CargoExecutable:          "/toolchain/bin/cargo",
		InstallerRustcExecutable: "/installer/bin/rustc",
		InstallerCargoExecutable: "/installer/bin/cargo",
		RustcVerboseVersion:      "rustc 1.95.0 (ed80dadd6a 2026-06-18)",
		Target:                   triple,
		Linker: provenanceLinker{
			Command:    "cc",
			Executable: "/usr/bin/cc",
			Version:    "cc 1.0",
		},
	}
}

func testHash(contents string) string {
	sum := sha256.Sum256([]byte(contents))
	return hex.EncodeToString(sum[:])
}
