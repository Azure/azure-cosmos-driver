// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	testBaseSHA   = "1111111111111111111111111111111111111111"
	testMergeSHA  = "2222222222222222222222222222222222222222"
	testTagSHA    = "3333333333333333333333333333333333333333"
	testSourceSHA = "4444444444444444444444444444444444444444"
)

func TestValidateVersion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		version string
		valid   bool
	}{
		{"0.1.0", true},
		{"1.0.0", true},
		{"v0.1.0", false},
		{"01.0.0", false},
		{"0.01.0", false},
		{"0.1.00", false},
		{"0.1.0-alpha", false},
		{"0.1.0+build", false},
		{"2.0.0", false},
	} {
		test := test
		t.Run(test.version, func(t *testing.T) {
			t.Parallel()
			_, err := validateVersion(test.version)
			if (err == nil) != test.valid {
				t.Fatalf("validateVersion(%q) error = %v, valid = %t", test.version, err, test.valid)
			}
		})
	}
}

func TestValidatePullRequest(t *testing.T) {
	t.Parallel()
	valid := testPullRequest()
	if err := validatePullRequest(valid, "Azure/azure-cosmos-driver", 14, "main"); err != nil {
		t.Fatalf("validatePullRequest() error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*pullRequest)
		want   string
	}{
		{"unmerged", func(pr *pullRequest) { pr.Merged = false }, "not merged"},
		{"wrong base", func(pr *pullRequest) { pr.Base.Ref = "release" }, "base branch"},
		{"missing merge SHA", func(pr *pullRequest) { pr.MergeCommitSHA = "" }, "merge commit SHA"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pr := valid
			test.mutate(&pr)
			err := validatePullRequest(pr, "Azure/azure-cosmos-driver", 14, "main")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validatePullRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunResolvePRWritesBlockedPlanForInvalidPR(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	metadataPath := filepath.Join(root, "pr.json")
	planPath := filepath.Join(root, "plan.json")
	summaryPath := filepath.Join(root, "summary.md")
	outputPath := filepath.Join(root, "github-output")
	writeFileForTest(t, outputPath, "")
	pr := testPullRequest()
	pr.Merged = false
	encodedPR, err := json.Marshal(pr)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{responses: []scriptedResponse{{
		name:     "gh",
		contains: "repos/Azure/azure-cosmos-driver/pulls/14",
		output:   string(encodedPR),
	}}}
	err = runResolvePR(context.Background(), []string{
		"-repository", "Azure/azure-cosmos-driver",
		"-pr-number", "14",
		"-default-branch", "main",
		"-version", "0.1.0",
		"-output", metadataPath,
		"-plan-output", planPath,
		"-summary", summaryPath,
		"-github-output", outputPath,
	}, runner)
	if err == nil || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("runResolvePR() error = %v, want not merged", err)
	}
	contents, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan releasePlan
	if err := json.Unmarshal(contents, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Status != "blocked" || len(plan.Modules) != len(expectedModulePaths) {
		t.Fatalf("blocked plan = %#v", plan)
	}
	if len(plan.Reasons) != 1 || !strings.Contains(plan.Reasons[0], "not merged") {
		t.Fatalf("blocked plan reasons = %v", plan.Reasons)
	}
}

func TestClassifyTags(t *testing.T) {
	t.Parallel()
	allAbsent := make(map[string]tagResolution)
	for _, modulePath := range expectedModulePaths {
		allAbsent[modulePath+"/v0.1.0"] = tagResolution{}
	}
	assertPlanStatus(t, allAbsent, "eligible_to_publish")

	allAtTarget := make(map[string]tagResolution)
	for _, modulePath := range expectedModulePaths {
		allAtTarget[modulePath+"/v0.1.0"] = tagResolution{
			Present:        true,
			Kind:           "lightweight",
			ReferenceSHA:   testMergeSHA,
			ResolvedCommit: testMergeSHA,
		}
	}
	assertPlanStatus(t, allAtTarget, "already_published")

	partial := cloneResolutions(allAtTarget)
	partial[expectedModulePaths[0]+"/v0.1.0"] = tagResolution{}
	assertPlanStatus(t, partial, "blocked")

	mismatched := cloneResolutions(allAtTarget)
	mismatched[expectedModulePaths[0]+"/v0.1.0"] = tagResolution{
		Present:        true,
		Kind:           "lightweight",
		ReferenceSHA:   testTagSHA,
		ResolvedCommit: testTagSHA,
	}
	assertPlanStatus(t, mismatched, "blocked")
}

func TestGitHubClientDereferencesAnnotatedTag(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{responses: []scriptedResponse{
		{
			name:     "gh",
			contains: "git/matching-refs/tags/linux/amd64/v0.1.0",
			output: fmt.Sprintf(
				`[{"ref":"refs/tags/linux/amd64/v0.1.0","object":{"sha":"%s","type":"tag"}}]`,
				testTagSHA,
			),
		},
		{
			name:     "gh",
			contains: "git/tags/" + testTagSHA,
			output:   fmt.Sprintf(`{"object":{"sha":"%s","type":"commit"}}`, testMergeSHA),
		},
	}}
	resolution, err := (githubClient{runner: runner}).Lookup(
		context.Background(),
		"Azure/azure-cosmos-driver",
		"linux/amd64/v0.1.0",
	)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if resolution.Kind != "annotated" || resolution.ResolvedCommit != testMergeSHA {
		t.Fatalf("Lookup() = %#v, want annotated tag at merge SHA", resolution)
	}
}

func TestValidateReleaseContract(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	manifest, err := validateReleaseContract(root, "0.1.0")
	if err != nil {
		t.Fatalf("validateReleaseContract() error = %v", err)
	}
	if manifest.SourceCommit != testSourceSHA {
		t.Fatalf("SourceCommit = %q, want %q", manifest.SourceCommit, testSourceSHA)
	}
	if manifest.RustDriverVersion != "0.8.0" {
		t.Fatalf("RustDriverVersion = %q, want independent Rust version 0.8.0", manifest.RustDriverVersion)
	}
}

func TestValidateReleaseContractRejectsModuleSetChanges(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*provenance)
		want   string
	}{
		{
			"missing module",
			func(manifest *provenance) { manifest.Targets = manifest.Targets[1:] },
			"missing=[windows/amd64]",
		},
		{
			"extra module",
			func(manifest *provenance) {
				manifest.Targets = append(manifest.Targets, provenanceTarget{ModulePath: "freebsd/amd64"})
			},
			"extra=[freebsd/amd64]",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := writeContractFixture(t, "0.1.0")
			manifest := readContractProvenance(t, root)
			test.mutate(&manifest)
			writeJSONForTest(t, filepath.Join(root, "provenance.json"), manifest)
			_, err := validateReleaseContract(root, "0.1.0")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateReleaseContract() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateReleaseContractRejectsInconsistentVersions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(string, *provenance)
		want   string
	}{
		{
			"native provenance",
			func(_ string, manifest *provenance) { manifest.NativeInterfaceVersion = "0.2.0" },
			"native interface version",
		},
		{
			"header",
			func(root string, _ *provenance) {
				writeFileForTest(
					t,
					filepath.Join(root, "linux", "amd64", "azurecosmosdriver.h"),
					"#define AZURECOSMOSDRIVER_H_VERSION \"0.2.0\"\n",
				)
			},
			"does not match native interface version",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := writeContractFixture(t, "0.1.0")
			manifest := readContractProvenance(t, root)
			test.mutate(root, &manifest)
			writeJSONForTest(t, filepath.Join(root, "provenance.json"), manifest)
			_, err := validateReleaseContract(root, "0.1.0")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateReleaseContract() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBuildReleasePlanUsesPRBaseForContinuity(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	runner := &gitAndValidatorRunner{
		root:          root,
		mergeSHA:      testMergeSHA,
		baseSHA:       testBaseSHA,
		remoteBaseSHA: "5555555555555555555555555555555555555555",
	}
	tags := staticTagReader{resolutions: absentTagResolutions("0.1.0")}
	plan, err := buildReleasePlan(context.Background(), planOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
	}, testPullRequest(), runner, tags)
	if err != nil {
		t.Fatalf("buildReleasePlan() error = %v", err)
	}
	if plan.IntegrityValidatorBaseRef != testBaseSHA {
		t.Fatalf("IntegrityValidatorBaseRef = %q, want %q", plan.IntegrityValidatorBaseRef, testBaseSHA)
	}
	wantArguments := []string{"integrity", "-root", root, "-base-ref", testBaseSHA}
	if !reflect.DeepEqual(runner.validatorArguments, wantArguments) {
		t.Fatalf("validator arguments = %v, want %v", runner.validatorArguments, wantArguments)
	}
}

func assertPlanStatus(t *testing.T, resolutions map[string]tagResolution, want string) {
	t.Helper()
	modules, status, _ := classifyTags("0.1.0", testMergeSHA, resolutions)
	if status != want {
		t.Fatalf("classifyTags() status = %q, want %q", status, want)
	}
	if len(modules) != len(expectedModulePaths) {
		t.Fatalf("classifyTags() returned %d modules, want %d", len(modules), len(expectedModulePaths))
	}
}

func cloneResolutions(source map[string]tagResolution) map[string]tagResolution {
	clone := make(map[string]tagResolution, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func absentTagResolutions(version string) map[string]tagResolution {
	resolutions := make(map[string]tagResolution, len(expectedModulePaths))
	for _, modulePath := range expectedModulePaths {
		resolutions[modulePath+"/v"+version] = tagResolution{}
	}
	return resolutions
}

func testPullRequest() pullRequest {
	var pr pullRequest
	pr.Number = 14
	pr.Merged = true
	pr.MergeCommitSHA = testMergeSHA
	pr.Base.Ref = "main"
	pr.Base.SHA = testBaseSHA
	pr.Base.Repo.FullName = "Azure/azure-cosmos-driver"
	return pr
}

func writeContractFixture(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	header := fmt.Sprintf("#define AZURECOSMOSDRIVER_H_VERSION \"%s\"\n", version)
	targets := make([]provenanceTarget, 0, len(expectedModulePaths))
	for _, modulePath := range expectedModulePaths {
		targets = append(targets, provenanceTarget{ModulePath: modulePath})
		writeFileForTest(t, filepath.Join(root, filepath.FromSlash(modulePath), "azurecosmosdriver.h"), header)
		writeFileForTest(
			t,
			filepath.Join(root, filepath.FromSlash(modulePath), "native", "azurecosmosdriver.h"),
			header,
		)
	}
	writeJSONForTest(t, filepath.Join(root, "provenance.json"), provenance{
		SchemaVersion:          1,
		SourceCommit:           testSourceSHA,
		NativeInterfaceCrate:   "azure_data_cosmos_driver_native",
		NativeInterfaceVersion: version,
		RustDriverCrate:        "azure_data_cosmos_driver",
		RustDriverVersion:      "0.8.0",
		Targets:                targets,
	})
	return root
}

func readContractProvenance(t *testing.T, root string) provenance {
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

func writeJSONForTest(t *testing.T, filename string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFileForTest(t, filename, string(contents)+"\n")
}

func writeFileForTest(t *testing.T, filename, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

type staticTagReader struct {
	resolutions map[string]tagResolution
	err         error
}

func (reader staticTagReader) Lookup(_ context.Context, _, tag string) (tagResolution, error) {
	if reader.err != nil {
		return tagResolution{}, reader.err
	}
	return reader.resolutions[tag], nil
}

type scriptedResponse struct {
	name     string
	contains string
	output   string
	err      error
}

type scriptedRunner struct {
	responses []scriptedResponse
}

func (runner *scriptedRunner) Run(
	_ context.Context,
	_ string,
	name string,
	arguments ...string,
) ([]byte, error) {
	if len(runner.responses) == 0 {
		return nil, errors.New("unexpected command")
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	joined := strings.Join(arguments, " ")
	if response.name != name || !strings.Contains(joined, response.contains) {
		return nil, fmt.Errorf("command %s %s did not match %#v", name, joined, response)
	}
	return []byte(response.output), response.err
}

type gitAndValidatorRunner struct {
	root               string
	mergeSHA           string
	baseSHA            string
	remoteBaseSHA      string
	validatorArguments []string
}

func (runner *gitAndValidatorRunner) Run(
	_ context.Context,
	_ string,
	name string,
	arguments ...string,
) ([]byte, error) {
	if name == "trusted-validator" {
		runner.validatorArguments = append([]string(nil), arguments...)
		return []byte("validated"), nil
	}
	if name != "git" || len(arguments) < 3 || arguments[0] != "-C" || arguments[1] != runner.root {
		return nil, fmt.Errorf("unexpected command: %s %v", name, arguments)
	}
	gitArguments := arguments[2:]
	joined := strings.Join(gitArguments, " ")
	switch {
	case joined == "rev-parse --verify HEAD^{commit}":
		return []byte(runner.mergeSHA + "\n"), nil
	case joined == "cat-file -e "+runner.mergeSHA+"^{commit}":
		return nil, nil
	case joined == "cat-file -e "+runner.baseSHA+"^{commit}":
		return nil, nil
	case joined == "merge-base --is-ancestor "+runner.baseSHA+" "+runner.mergeSHA:
		return nil, nil
	case joined == "rev-parse --verify refs/remotes/origin/main^{commit}":
		return []byte(runner.remoteBaseSHA + "\n"), nil
	case joined == "merge-base --is-ancestor "+runner.mergeSHA+" refs/remotes/origin/main":
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected git command: %s", joined)
	}
}
