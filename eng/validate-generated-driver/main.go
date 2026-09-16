// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const moduleRoot = "github.com/Azure/azure-cosmos-driver"

var (
	sha256Line = regexp.MustCompile(`^([0-9a-fA-F]{64})  ([^[:cntrl:]]+)$`)
	sha256Hex  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitHex  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type provenance struct {
	SchemaVersion          int                 `json:"schema_version"`
	SourceCommit           string              `json:"source_commit"`
	NativeInterfaceCrate   string              `json:"native_interface_crate"`
	NativeInterfaceVersion string              `json:"native_interface_version"`
	RustDriverCrate        string              `json:"rust_driver_crate"`
	RustDriverVersion      string              `json:"rust_driver_version"`
	RustToolchain          provenanceToolchain `json:"rust_toolchain"`
	Targets                []provenanceTarget  `json:"targets"`
}

type provenanceTarget struct {
	ID                  string          `json:"id"`
	Triple              string          `json:"triple"`
	ModulePath          string          `json:"module_path"`
	StaticLibraryPath   string          `json:"static_library_path"`
	StaticLibrarySHA256 string          `json:"static_library_sha256"`
	HeaderSHA256        string          `json:"header_sha256"`
	Toolchain           targetToolchain `json:"toolchain"`
}

type provenanceToolchain struct {
	Provider                string `json:"provider"`
	Manager                 string `json:"manager"`
	ManagerVersion          string `json:"manager_version"`
	Channel                 string `json:"channel"`
	InstallerPackageVersion string `json:"installer_package_version"`
	RustcRelease            string `json:"rustc_release"`
	RustcCommitHash         string `json:"rustc_commit_hash"`
	CargoVersion            string `json:"cargo_version"`
}

type targetToolchain struct {
	SelectedToolchain        string           `json:"selected_toolchain"`
	Sysroot                  string           `json:"sysroot"`
	RustcExecutable          string           `json:"rustc_executable"`
	CargoExecutable          string           `json:"cargo_executable"`
	InstallerRustcExecutable string           `json:"installer_rustc_executable"`
	InstallerCargoExecutable string           `json:"installer_cargo_executable"`
	RustcVerboseVersion      string           `json:"rustc_verbose_version"`
	Target                   string           `json:"target"`
	Linker                   provenanceLinker `json:"linker"`
}

type provenanceLinker struct {
	Command    string `json:"command"`
	Executable string `json:"executable"`
	Version    string `json:"version"`
}

type repository struct {
	root       string
	provenance provenance
	modules    []string
}

func main() {
	if len(os.Args) < 2 {
		exitError(errors.New("usage: go run ./eng/validate-generated-driver/main.go <integrity|native-smoke> [-root path]"))
	}

	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "repository root")
	baseRef := flags.String("base-ref", "", "Git base revision used to prevent removal of published targets")
	if err := flags.Parse(os.Args[2:]); err != nil {
		exitError(err)
	}

	var err error
	switch command {
	case "integrity":
		err = validateIntegrityAgainstBase(*root, *baseRef, os.Stdout)
	case "native-smoke":
		err = validateNativeSmoke(*root, os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		exitError(err)
	}
}

func exitError(err error) {
	fmt.Fprintf(os.Stderr, "validation failed: %v\n", err)
	os.Exit(1)
}

func validateIntegrity(root string, output io.Writer) error {
	return validateIntegrityAgainstBase(root, "", output)
}

func validateIntegrityAgainstBase(root, baseRef string, output io.Writer) error {
	repo, empty, err := loadRepository(root)
	if err != nil {
		return err
	}
	baseProvenance, hasBaseProvenance, err := readProvenanceAtRef(repo.root, baseRef)
	if err != nil {
		return err
	}
	if empty {
		if hasBaseProvenance {
			return errors.New("generated release cannot be deleted after it has been published on the base branch")
		}
		fmt.Fprintln(output, "No generated Go modules or release manifests are present; integrity validation has nothing to check.")
		return nil
	}

	targetsByModule := make(map[string]provenanceTarget, len(repo.provenance.Targets))
	targetIDs := make(map[string]struct{}, len(repo.provenance.Targets))
	targetTriples := make(map[string]struct{}, len(repo.provenance.Targets))
	var releaseHeaderHash string
	for _, target := range repo.provenance.Targets {
		if target.ID == "" || target.Triple == "" {
			return errors.New("every provenance target must have a non-empty id and triple")
		}
		if _, exists := targetIDs[target.ID]; exists {
			return fmt.Errorf("provenance contains duplicate target id %q", target.ID)
		}
		targetIDs[target.ID] = struct{}{}
		if _, exists := targetTriples[target.Triple]; exists {
			return fmt.Errorf("provenance contains duplicate target triple %q", target.Triple)
		}
		targetTriples[target.Triple] = struct{}{}
		if err := validateModulePath(target.ModulePath); err != nil {
			return fmt.Errorf("target %q: %w", target.ID, err)
		}
		if _, exists := targetsByModule[target.ModulePath]; exists {
			return fmt.Errorf("provenance contains duplicate module path %q", target.ModulePath)
		}
		targetsByModule[target.ModulePath] = target
		if !sha256Hex.MatchString(target.StaticLibrarySHA256) {
			return fmt.Errorf("target %q has an invalid lowercase static_library_sha256", target.ID)
		}
		if !sha256Hex.MatchString(target.HeaderSHA256) {
			return fmt.Errorf("target %q has an invalid lowercase header_sha256", target.ID)
		}
		if err := validateTargetToolchain(repo.provenance.RustToolchain, target); err != nil {
			return fmt.Errorf("target %q toolchain: %w", target.ID, err)
		}
		if releaseHeaderHash == "" {
			releaseHeaderHash = target.HeaderSHA256
		} else if target.HeaderSHA256 != releaseHeaderHash {
			return fmt.Errorf("target %q header hash differs from the other targets", target.ID)
		}
	}

	if hasBaseProvenance {
		if err := validateContinuity(baseProvenance, repo.provenance); err != nil {
			return err
		}
	}

	moduleSet := make(map[string]struct{}, len(repo.modules))
	for _, modulePath := range repo.modules {
		moduleSet[modulePath] = struct{}{}
		target, declared := targetsByModule[modulePath]
		if !declared {
			return fmt.Errorf("nested module %q is missing from provenance.json", modulePath)
		}
		if err := validateModule(repo.root, target); err != nil {
			return err
		}
	}
	for modulePath := range targetsByModule {
		if _, exists := moduleSet[modulePath]; !exists {
			return fmt.Errorf("provenance target module %q has no go.mod", modulePath)
		}
	}
	if err := validateGeneratedLayout(repo.root, repo.provenance.Targets); err != nil {
		return err
	}

	if err := validateChecksums(repo); err != nil {
		return err
	}

	fmt.Fprintf(output, "Validated %d generated module(s), provenance.json, SHA256SUMS, Go formatting, and module metadata.\n", len(repo.modules))
	return nil
}

func loadRepository(root string) (repository, bool, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return repository{}, false, fmt.Errorf("resolve repository root: %w", err)
	}
	modules, err := discoverModules(absoluteRoot)
	if err != nil {
		return repository{}, false, err
	}

	provenancePath := filepath.Join(absoluteRoot, "provenance.json")
	checksumsPath := filepath.Join(absoluteRoot, "SHA256SUMS")
	hasProvenance := isRegularFile(provenancePath)
	hasChecksums := isRegularFile(checksumsPath)
	if len(modules) == 0 && !hasProvenance && !hasChecksums {
		return repository{root: absoluteRoot}, true, nil
	}
	if len(modules) == 0 {
		return repository{}, false, errors.New("release manifests exist but no nested Go modules were found")
	}
	if !hasProvenance || !hasChecksums {
		return repository{}, false, errors.New("generated modules require both root provenance.json and SHA256SUMS")
	}

	manifest, err := readProvenance(provenancePath)
	if err != nil {
		return repository{}, false, err
	}
	return repository{root: absoluteRoot, provenance: manifest, modules: modules}, false, nil
}

func readProvenanceAtRef(root, ref string) (provenance, bool, error) {
	if ref == "" {
		return provenance{}, false, nil
	}

	command := exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", ref, "--", "provenance.json")
	output, err := command.CombinedOutput()
	if err != nil {
		return provenance{}, false, fmt.Errorf("inspect provenance.json at base ref %q: %w\n%s", ref, err, output)
	}
	if strings.TrimSpace(string(output)) == "" {
		return provenance{}, false, nil
	}

	command = exec.Command("git", "-C", root, "show", ref+":provenance.json")
	output, err = command.CombinedOutput()
	if err != nil {
		return provenance{}, false, fmt.Errorf("read provenance.json at base ref %q: %w\n%s", ref, err, output)
	}
	manifest, err := decodeProvenanceDocument(bytes.NewReader(output), true)
	if err != nil {
		return provenance{}, false, fmt.Errorf("parse provenance.json at base ref %q: %w", ref, err)
	}
	return manifest, true, nil
}

func discoverModules(root string) ([]string, error) {
	var modules []string
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if isGeneratedPath(relative) {
				return fmt.Errorf("generated path is a symbolic link: %q", relative)
			}
			return nil
		}
		if entry.Name() != "go.mod" || entry.IsDir() {
			return nil
		}
		relativeDirectory, err := filepath.Rel(root, filepath.Dir(current))
		if err != nil {
			return err
		}
		modulePath := filepath.ToSlash(relativeDirectory)
		if modulePath == "." {
			return errors.New("the repository root go.mod is not a generated target module")
		}
		modules = append(modules, modulePath)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover nested Go modules: %w", err)
	}
	sort.Strings(modules)
	return modules, nil
}

func readProvenance(filename string) (provenance, error) {
	file, err := os.Open(filename)
	if err != nil {
		return provenance{}, fmt.Errorf("open provenance.json: %w", err)
	}
	defer file.Close()

	manifest, err := decodeProvenance(file)
	if err != nil {
		return provenance{}, fmt.Errorf("parse provenance.json: %w", err)
	}
	return manifest, nil
}

func decodeProvenance(reader io.Reader) (provenance, error) {
	return decodeProvenanceDocument(reader, false)
}

func decodeProvenanceDocument(reader io.Reader, allowLegacySchema bool) (provenance, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest provenance
	if err := decoder.Decode(&manifest); err != nil {
		return provenance{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return provenance{}, err
	}
	if manifest.SchemaVersion != 2 && !(allowLegacySchema && manifest.SchemaVersion == 1) {
		return provenance{}, fmt.Errorf("unsupported provenance schema_version %d", manifest.SchemaVersion)
	}
	if !commitHex.MatchString(manifest.SourceCommit) {
		return provenance{}, errors.New("provenance source_commit must be a lowercase 40-character Git commit")
	}
	if manifest.NativeInterfaceVersion == "" || manifest.RustDriverVersion == "" {
		return provenance{}, errors.New("provenance release identity fields must not be empty")
	}
	if manifest.NativeInterfaceCrate != "azure_data_cosmos_driver_native" {
		return provenance{}, fmt.Errorf("unexpected native_interface_crate %q", manifest.NativeInterfaceCrate)
	}
	if manifest.RustDriverCrate != "azure_data_cosmos_driver" {
		return provenance{}, fmt.Errorf("unexpected rust_driver_crate %q", manifest.RustDriverCrate)
	}
	if manifest.SchemaVersion == 2 {
		if err := validateProvenanceToolchain(manifest.RustToolchain); err != nil {
			return provenance{}, fmt.Errorf("provenance rust_toolchain: %w", err)
		}
	}
	if len(manifest.Targets) == 0 {
		return provenance{}, errors.New("provenance targets must not be empty")
	}
	return manifest, nil
}

func validateProvenanceToolchain(toolchain provenanceToolchain) error {
	required := map[string]string{
		"provider":                  toolchain.Provider,
		"manager":                   toolchain.Manager,
		"manager_version":           toolchain.ManagerVersion,
		"channel":                   toolchain.Channel,
		"installer_package_version": toolchain.InstallerPackageVersion,
		"rustc_release":             toolchain.RustcRelease,
		"cargo_version":             toolchain.CargoVersion,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	if !commitHex.MatchString(toolchain.RustcCommitHash) {
		return errors.New("rustc_commit_hash must be a lowercase 40-character Git commit")
	}
	return nil
}

func validateTargetToolchain(release provenanceToolchain, target provenanceTarget) error {
	toolchain := target.Toolchain
	required := map[string]string{
		"selected_toolchain":         toolchain.SelectedToolchain,
		"sysroot":                    toolchain.Sysroot,
		"rustc_executable":           toolchain.RustcExecutable,
		"cargo_executable":           toolchain.CargoExecutable,
		"installer_rustc_executable": toolchain.InstallerRustcExecutable,
		"installer_cargo_executable": toolchain.InstallerCargoExecutable,
		"rustc_verbose_version":      toolchain.RustcVerboseVersion,
		"target":                     toolchain.Target,
		"linker.command":             toolchain.Linker.Command,
		"linker.executable":          toolchain.Linker.Executable,
		"linker.version":             toolchain.Linker.Version,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	if toolchain.SelectedToolchain != release.Channel {
		return fmt.Errorf(
			"selected_toolchain %q does not match release channel %q",
			toolchain.SelectedToolchain,
			release.Channel,
		)
	}
	if toolchain.Target != target.Triple {
		return fmt.Errorf("target %q does not match target triple %q", toolchain.Target, target.Triple)
	}
	return nil
}

func validateContinuity(base, current provenance) error {
	currentTargets := make(map[string]provenanceTarget, len(current.Targets))
	for _, target := range current.Targets {
		currentTargets[target.ID] = target
	}
	for _, baseTarget := range base.Targets {
		currentTarget, exists := currentTargets[baseTarget.ID]
		if !exists {
			return fmt.Errorf("previously published target %q cannot be removed", baseTarget.ID)
		}
		if currentTarget.ModulePath != baseTarget.ModulePath {
			return fmt.Errorf("previously published target %q module path cannot change from %q to %q", baseTarget.ID, baseTarget.ModulePath, currentTarget.ModulePath)
		}
		if currentTarget.Triple != baseTarget.Triple {
			return fmt.Errorf("previously published target %q triple cannot change from %q to %q", baseTarget.ID, baseTarget.Triple, currentTarget.Triple)
		}
	}
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected data after JSON document")
		}
		return err
	}
	return nil
}

func validateModulePath(modulePath string) error {
	parts := strings.Split(modulePath, "/")
	if modulePath == "" ||
		strings.Contains(modulePath, `\`) ||
		path.IsAbs(modulePath) ||
		path.Clean(modulePath) != modulePath ||
		strings.HasPrefix(modulePath, "../") ||
		len(parts) != 2 ||
		parts[1] == "" {
		return fmt.Errorf("unsafe module_path %q", modulePath)
	}
	switch parts[0] {
	case "windows", "linux", "darwin":
	default:
		return fmt.Errorf("module_path %q is outside the expected platform roots", modulePath)
	}
	return nil
}

func validateGeneratedLayout(root string, targets []provenanceTarget) error {
	expected := make(map[string]struct{}, len(targets)*4)
	for _, target := range targets {
		parts := strings.Split(target.ModulePath, "/")
		goarch, _, _ := strings.Cut(parts[1], "-")
		for _, relative := range []string{
			path.Join(target.ModulePath, "go.mod"),
			path.Join(target.ModulePath, fmt.Sprintf("link_%s_%s.go", parts[0], goarch)),
			path.Join(target.ModulePath, "azurecosmosdriver.h"),
			path.Join(target.ModulePath, "libazurecosmosdriver.a"),
		} {
			expected[relative] = struct{}{}
		}
	}

	for _, platform := range []string{"windows", "linux", "darwin"} {
		platformRoot := filepath.Join(root, platform)
		if _, err := os.Lstat(platformRoot); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect generated root %q: %w", platform, err)
		}
		err := filepath.WalkDir(platformRoot, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("generated path is a symbolic link: %q", relative)
			}
			if entry.IsDir() {
				if strings.HasSuffix(relative, "/native") {
					modulePath := strings.TrimSuffix(relative, "/native")
					for _, target := range targets {
						if target.ModulePath == modulePath {
							return fmt.Errorf("target %q contains the removed native directory", target.ID)
						}
					}
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("generated path is not a regular file: %q", relative)
			}
			if _, allowed := expected[relative]; !allowed {
				return fmt.Errorf("unexpected file under generated platform roots: %q", relative)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func validateModule(root string, target provenanceTarget) error {
	moduleDirectory := filepath.Join(root, filepath.FromSlash(target.ModulePath))
	staticLibraryRelative, err := staticLibraryPath(target)
	if err != nil {
		return fmt.Errorf("target %q: %w", target.ID, err)
	}
	requiredFiles := map[string]string{
		"static library": filepath.Join(root, filepath.FromSlash(staticLibraryRelative)),
		"root header":    filepath.Join(moduleDirectory, "azurecosmosdriver.h"),
	}
	for description, filename := range requiredFiles {
		if err := requireRegularFile(root, filename); err != nil {
			return fmt.Errorf("target %q %s: %w", target.ID, description, err)
		}
	}

	hashChecks := []struct {
		description string
		filename    string
		expected    string
	}{
		{"static library", requiredFiles["static library"], target.StaticLibrarySHA256},
		{"root header", requiredFiles["root header"], target.HeaderSHA256},
	}
	for _, check := range hashChecks {
		actual, err := hashFile(check.filename)
		if err != nil {
			return err
		}
		if actual != check.expected {
			return fmt.Errorf("target %q %s SHA256 mismatch: provenance=%s file=%s", target.ID, check.description, check.expected, actual)
		}
	}

	linkFiles, err := filepath.Glob(filepath.Join(moduleDirectory, "link_*.go"))
	if err != nil {
		return fmt.Errorf("find target %q link file: %w", target.ID, err)
	}
	if len(linkFiles) != 1 {
		return fmt.Errorf("target %q must contain exactly one generated link_*.go file; found %d", target.ID, len(linkFiles))
	}
	if err := validateLinkFile(target, linkFiles[0]); err != nil {
		return err
	}
	if err := validateGoModule(target.ModulePath, moduleDirectory); err != nil {
		return err
	}
	return nil
}

func validateLinkFile(target provenanceTarget, filename string) error {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read target %q link file: %w", target.ID, err)
	}

	parts := strings.Split(target.ModulePath, "/")
	if len(parts) < 2 {
		return fmt.Errorf("target %q module_path does not identify GOOS/GOARCH", target.ID)
	}
	goos := parts[0]
	goarch, _, _ := strings.Cut(parts[1], "-")
	expectedName := fmt.Sprintf("link_%s_%s.go", goos, goarch)
	if filepath.Base(filename) != expectedName {
		return fmt.Errorf("target %q link filename must be %q", target.ID, expectedName)
	}
	requiredText := []string{
		"// Code generated by New-GoModules.ps1; DO NOT EDIT.",
		fmt.Sprintf("//go:build cgo && %s && %s", goos, goarch),
		`#include "azurecosmosdriver.h"`,
		`import "C"`,
	}
	for _, required := range requiredText {
		if !bytes.Contains(contents, []byte(required)) {
			return fmt.Errorf("target %q link file %q is missing %q", target.ID, filepath.Base(filename), required)
		}
	}
	ldflags, err := parseCgoLDFlags(contents)
	if err != nil {
		return fmt.Errorf("target %q link file %q: %w", target.ID, filepath.Base(filename), err)
	}
	hasModuleSearchPath := false
	hasDriverLibrary := false
	for _, ldflag := range ldflags {
		switch ldflag {
		case "-L${SRCDIR}":
			hasModuleSearchPath = true
		case "-lazurecosmosdriver":
			hasDriverLibrary = true
		default:
			if strings.HasPrefix(ldflag, "-L${SRCDIR}") {
				return fmt.Errorf(
					"target %q link file %q contains unexpected source-relative library search path %q",
					target.ID,
					filepath.Base(filename),
					ldflag,
				)
			}
		}
	}
	if !hasModuleSearchPath {
		return fmt.Errorf("target %q link file %q is missing exact linker flag %q", target.ID, filepath.Base(filename), "-L${SRCDIR}")
	}
	if !hasDriverLibrary {
		return fmt.Errorf("target %q link file %q is missing exact linker flag %q", target.ID, filepath.Base(filename), "-lazurecosmosdriver")
	}

	command := exec.Command("gofmt", "-d", filename)
	diff, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gofmt target %q link file: %w\n%s", target.ID, err, diff)
	}
	if len(diff) != 0 {
		return fmt.Errorf("target %q link file is not gofmt-formatted:\n%s", target.ID, diff)
	}
	return nil
}

func parseCgoLDFlags(contents []byte) ([]string, error) {
	const prefix = "// #cgo LDFLAGS:"
	var flags []string
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if flags != nil {
			return nil, errors.New("contains multiple #cgo LDFLAGS directives")
		}
		flags = strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
	}
	if flags == nil {
		return nil, errors.New("is missing a #cgo LDFLAGS directive")
	}
	return flags, nil
}

func validateGoModule(modulePath, moduleDirectory string) error {
	command := exec.Command("go", "mod", "edit", "-json")
	command.Dir = moduleDirectory
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("validate %q go.mod: %w\n%s", modulePath, err, output)
	}
	var metadata struct {
		Module struct {
			Path string
		} `json:"Module"`
	}
	if err := json.Unmarshal(output, &metadata); err != nil {
		return fmt.Errorf("parse %q go.mod metadata: %w", modulePath, err)
	}
	expected := moduleRoot + "/" + modulePath
	if metadata.Module.Path != expected {
		return fmt.Errorf("module %q declares %q; expected %q", modulePath, metadata.Module.Path, expected)
	}

	parts := strings.Split(modulePath, "/")
	goarch, _, _ := strings.Cut(parts[1], "-")
	command = exec.Command("go", "list", "./...")
	command.Dir = moduleDirectory
	command.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS="+parts[0], "GOARCH="+goarch)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("go list %q packages: %w\n%s", modulePath, err, output)
	}
	return nil
}

func validateChecksums(repo repository) error {
	expected := make(map[string]string, len(repo.provenance.Targets))
	for _, target := range repo.provenance.Targets {
		relative, err := staticLibraryPath(target)
		if err != nil {
			return fmt.Errorf("target %q: %w", target.ID, err)
		}
		expected[relative] = target.StaticLibrarySHA256
	}

	file, err := os.Open(filepath.Join(repo.root, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("open SHA256SUMS: %w", err)
	}
	defer file.Close()

	actual := make(map[string]string, len(expected))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		matches := sha256Line.FindStringSubmatch(line)
		if matches == nil {
			return fmt.Errorf("invalid SHA256SUMS entry %q", line)
		}
		relative := matches[2]
		if err := validateRelativeFilePath(relative); err != nil {
			return fmt.Errorf("SHA256SUMS: %w", err)
		}
		if _, duplicate := actual[relative]; duplicate {
			return fmt.Errorf("SHA256SUMS contains duplicate path %q", relative)
		}
		recorded := strings.ToLower(matches[1])
		provenanceHash, declared := expected[relative]
		if !declared {
			return fmt.Errorf("SHA256SUMS contains undeclared archive %q", relative)
		}
		if recorded != provenanceHash {
			return fmt.Errorf("SHA256SUMS and provenance.json disagree for %q", relative)
		}
		filename := filepath.Join(repo.root, filepath.FromSlash(relative))
		if err := requireRegularFile(repo.root, filename); err != nil {
			return err
		}
		fileHash, err := hashFile(filename)
		if err != nil {
			return err
		}
		if fileHash != recorded {
			return fmt.Errorf("SHA256SUMS mismatch for %q: manifest=%s file=%s", relative, recorded, fileHash)
		}
		actual[relative] = recorded
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SHA256SUMS: %w", err)
	}
	for relative := range expected {
		if _, exists := actual[relative]; !exists {
			return fmt.Errorf("SHA256SUMS is missing archive %q", relative)
		}
	}
	return nil
}

func staticLibraryPath(target provenanceTarget) (string, error) {
	expected := path.Join(target.ModulePath, "libazurecosmosdriver.a")
	if target.StaticLibraryPath != expected {
		return "", fmt.Errorf(
			"static_library_path is %q; expected %q",
			target.StaticLibraryPath,
			expected,
		)
	}
	if err := validateRelativeFilePath(target.StaticLibraryPath); err != nil {
		return "", fmt.Errorf("static_library_path: %w", err)
	}
	return target.StaticLibraryPath, nil
}

func validateRelativeFilePath(relative string) error {
	if relative == "" ||
		strings.Contains(relative, `\`) ||
		path.IsAbs(relative) ||
		path.Clean(relative) != relative ||
		strings.HasPrefix(relative, "../") {
		return fmt.Errorf("unsafe path %q", relative)
	}
	return nil
}

func requireRegularFile(root, filename string) error {
	relative, err := filepath.Rel(root, filename)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes repository root: %q", filename)
	}

	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("required file %q: %w", filepath.ToSlash(relative), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("generated path is a symbolic link: %q", filepath.ToSlash(relative))
		}
	}
	if !isRegularFile(filename) {
		return fmt.Errorf("required path is not a regular file: %q", filepath.ToSlash(relative))
	}
	return nil
}

func isRegularFile(filename string) bool {
	info, err := os.Stat(filename)
	return err == nil && info.Mode().IsRegular()
}

func isGeneratedPath(relative string) bool {
	first, _, _ := strings.Cut(relative, "/")
	switch first {
	case "windows", "linux", "darwin", "SHA256SUMS", "provenance.json":
		return true
	default:
		return false
	}
}

func hashFile(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", filename, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %q: %w", filename, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateNativeSmoke(root string, output io.Writer) error {
	repo, empty, err := loadRepository(root)
	if err != nil {
		return err
	}
	if empty {
		fmt.Fprintln(output, "No generated modules are present; native consumer validation has nothing to check.")
		return nil
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		fmt.Fprintf(output, "Native consumer validation requires Linux AMD64; current host is %s/%s.\n", runtime.GOOS, runtime.GOARCH)
		return nil
	}

	var hostTarget *provenanceTarget
	for index := range repo.provenance.Targets {
		target := &repo.provenance.Targets[index]
		if target.ModulePath == "linux/amd64" {
			hostTarget = target
			break
		}
	}
	if hostTarget == nil {
		return errors.New("generated release is missing the Linux AMD64 glibc module required for native CI validation")
	}
	if hostTarget.Triple != "x86_64-unknown-linux-gnu" {
		return fmt.Errorf("linux/amd64 target has unexpected Rust triple %q", hostTarget.Triple)
	}

	workDirectory, err := os.MkdirTemp("", "cosmos-driver-consumer-*")
	if err != nil {
		return fmt.Errorf("create consumer directory: %w", err)
	}
	defer os.RemoveAll(workDirectory)

	moduleDirectory := filepath.Join(repo.root, "linux", "amd64")
	moduleImport := moduleRoot + "/linux/amd64"
	goMod := fmt.Sprintf("module cosmos-driver-validation\n\ngo 1.25.0\n\nrequire %s v0.0.0\n\nreplace %s => %s\n", moduleImport, moduleImport, filepath.ToSlash(moduleDirectory))
	if err := os.WriteFile(filepath.Join(workDirectory, "go.mod"), []byte(goMod), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workDirectory, "main.go"), []byte(consumerSource(moduleImport)), 0o600); err != nil {
		return err
	}

	headerSource := filepath.Join(moduleDirectory, "azurecosmosdriver.h")
	if err := copyFile(headerSource, filepath.Join(workDirectory, "azurecosmosdriver.h")); err != nil {
		return err
	}

	if err := runGo(workDirectory, "run", "-mod=mod", "."); err != nil {
		return err
	}
	if err := runGo(workDirectory, "mod", "vendor"); err != nil {
		return err
	}
	if err := validateVendoredModule(workDirectory, moduleImport, moduleDirectory); err != nil {
		return err
	}
	if err := runGo(workDirectory, "run", "-mod=vendor", "."); err != nil {
		return err
	}
	fmt.Fprintln(output, "Linux AMD64 direct and vendored consumers linked, ran, and matched the native ABI version.")
	return nil
}

func validateVendoredModule(workDirectory, moduleImport, moduleDirectory string) error {
	vendoredModule := filepath.Join(workDirectory, "vendor", filepath.FromSlash(moduleImport))
	for _, relative := range []string{"azurecosmosdriver.h", "libazurecosmosdriver.a"} {
		source := filepath.Join(moduleDirectory, relative)
		vendored := filepath.Join(vendoredModule, relative)
		if err := requireRegularFile(workDirectory, vendored); err != nil {
			return fmt.Errorf("vendored module %q: %w", moduleImport, err)
		}
		sourceHash, err := hashFile(source)
		if err != nil {
			return err
		}
		vendoredHash, err := hashFile(vendored)
		if err != nil {
			return err
		}
		if sourceHash != vendoredHash {
			return fmt.Errorf("vendored module %q file %q differs from the generated module", moduleImport, relative)
		}
	}

	legacyArchive := filepath.Join(vendoredModule, "native", "libazurecosmosdriver.a")
	if _, err := os.Lstat(legacyArchive); err == nil {
		return fmt.Errorf("vendored module %q contains an obsolete nested archive", moduleImport)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect obsolete vendored archive: %w", err)
	}
	return nil
}

func consumerSource(moduleImport string) string {
	return fmt.Sprintf(`package main

/*
#cgo CFLAGS: -I${SRCDIR}
#include "azurecosmosdriver.h"
static const char *header_version(void) {
	return AZURECOSMOSDRIVER_H_VERSION;
}
*/
import "C"

import (
	"fmt"
	_ %q
)

func main() {
	runtimeVersion := C.GoString(C.cosmos_version())
	headerVersion := C.GoString(C.header_version())
	if runtimeVersion == "" || runtimeVersion != headerVersion {
		panic(fmt.Sprintf("native/header version mismatch: native=%%q header=%%q", runtimeVersion, headerVersion))
	}
	fmt.Printf("azurecosmosdriver ABI version %%s\n", runtimeVersion)
}
`, moduleImport)
}

func runGo(directory string, arguments ...string) error {
	command := exec.Command("go", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "CGO_ENABLED=1")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go %s failed: %w\n%s", strings.Join(arguments, " "), err, output)
	}
	return nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %q: %w", source, err)
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("create %q: %w", destination, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy %q: %w", source, err)
	}
	return output.Close()
}
