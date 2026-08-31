//go:build cgo && windows && amd64

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticLink(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "azurecosmosdriver-smoke.exe")
	build := exec.Command("go", "build", "-trimpath", "-o", executable, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build smoke executable: %v\n%s", err, output)
	}

	if output, err := exec.Command(executable).CombinedOutput(); err != nil {
		t.Fatalf("run smoke executable: %v\n%s", err, output)
	}

	objdump := findObjdump(t)
	output, err := exec.Command(objdump, "-p", executable).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect smoke executable dependencies: %v\n%s", err, output)
	}

	var dependencies []string
	for _, line := range strings.Split(string(output), "\n") {
		index := strings.Index(strings.ToLower(line), "dll name:")
		if index < 0 {
			continue
		}

		dependency := strings.TrimSpace(line[index+len("dll name:"):])
		dependencies = append(dependencies, dependency)
		if strings.HasPrefix(strings.ToLower(dependency), "lib") {
			t.Errorf("unexpected non-system runtime dependency %q", dependency)
		}
	}
	if len(dependencies) == 0 {
		t.Fatal("objdump reported no DLL dependencies")
	}
	t.Logf("DLL dependencies: %s", strings.Join(dependencies, ", "))
}

func findObjdump(t *testing.T) string {
	t.Helper()

	if path, err := exec.LookPath("objdump"); err == nil {
		return path
	}

	cc := os.Getenv("CC")
	if cc == "" {
		output, err := exec.Command("go", "env", "CC").Output()
		if err != nil {
			t.Skipf("objdump is unavailable and go env CC failed: %v", err)
		}
		cc = strings.TrimSpace(string(output))
	}

	ccPath, err := exec.LookPath(cc)
	if err == nil {
		candidate := filepath.Join(filepath.Dir(ccPath), "objdump.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	t.Skip("objdump is unavailable; skipping runtime DLL inspection")
	return ""
}
