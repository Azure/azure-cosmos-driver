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
	"runtime"
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

func TestGitHubClientRecursivelyDereferencesAnnotatedTag(t *testing.T) {
	t.Parallel()
	nestedTagSHA := "5555555555555555555555555555555555555555"
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
			output:   fmt.Sprintf(`{"object":{"sha":"%s","type":"tag"}}`, nestedTagSHA),
		},
		{
			name:     "gh",
			contains: "git/tags/" + nestedTagSHA,
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
		t.Fatalf("Lookup() = %#v, want recursively peeled annotated tag at merge SHA", resolution)
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
	runner := newGitAndValidatorRunner(root)
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

func TestBuildReleasePlanRejectsInvalidCandidateGitState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		configure func(*gitAndValidatorRunner)
		want      string
	}{
		{
			name: "candidate HEAD differs from merge SHA",
			configure: func(runner *gitAndValidatorRunner) {
				runner.headSHA = testTagSHA
			},
			want: "candidate HEAD is " + testTagSHA + ", expected derived merge SHA " + testMergeSHA,
		},
		{
			name: "merge SHA is absent or not a commit",
			configure: func(runner *gitAndValidatorRunner) {
				runner.failGitCommand(
					"cat-file -e "+testMergeSHA+"^{commit}",
					errors.New("merge object is unavailable"),
				)
			},
			want: "derived merge SHA is not a commit",
		},
		{
			name: "base SHA is absent or not a commit",
			configure: func(runner *gitAndValidatorRunner) {
				runner.failGitCommand(
					"cat-file -e "+testBaseSHA+"^{commit}",
					errors.New("base object is unavailable"),
				)
			},
			want: "pull request base SHA is not a commit",
		},
		{
			name: "base SHA is not an ancestor of merge SHA",
			configure: func(runner *gitAndValidatorRunner) {
				runner.failGitCommand(
					"merge-base --is-ancestor "+testBaseSHA+" "+testMergeSHA,
					errors.New("not an ancestor"),
				)
			},
			want: "pull request base SHA is not an ancestor of the derived merge commit",
		},
		{
			name: "origin default branch is missing",
			configure: func(runner *gitAndValidatorRunner) {
				runner.failGitCommand(
					"rev-parse --verify refs/remotes/origin/main^{commit}",
					errors.New("remote ref is unavailable"),
				)
			},
			want: "current remote default branch is unavailable",
		},
		{
			name: "merge SHA is not reachable from origin default branch",
			configure: func(runner *gitAndValidatorRunner) {
				runner.failGitCommand(
					"merge-base --is-ancestor "+testMergeSHA+" refs/remotes/origin/main",
					errors.New("merge is not reachable"),
				)
			},
			want: `derived merge SHA is not reachable from current remote default branch "main"`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := writeContractFixture(t, "0.1.0")
			runner := newGitAndValidatorRunner(root)
			test.configure(runner)

			plan, err := buildReleasePlan(context.Background(), planOptions{
				Root:          root,
				Repository:    "Azure/azure-cosmos-driver",
				Version:       "0.1.0",
				ValidatorPath: "trusted-validator",
			}, testPullRequest(), runner, staticTagReader{
				resolutions: absentTagResolutions("0.1.0"),
			})

			if err == nil {
				t.Fatal("buildReleasePlan() error = nil, want candidate Git-state failure")
			}
			if plan.Status != "blocked" {
				t.Fatalf("buildReleasePlan() status = %q, want blocked", plan.Status)
			}
			if len(plan.Reasons) != 1 || !strings.Contains(plan.Reasons[0], test.want) {
				t.Fatalf("buildReleasePlan() reasons = %v, want %q", plan.Reasons, test.want)
			}
			if len(runner.validatorArguments) != 0 {
				t.Fatalf("validator ran after candidate Git-state failure with arguments %v", runner.validatorArguments)
			}
		})
	}
}

func TestRunPlanCommandWritesBlockedPlanForCandidateHEADMismatch(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	metadataPath := filepath.Join(root, "pr-metadata.json")
	planPath := filepath.Join(root, "release-plan.json")
	summaryPath := filepath.Join(root, "summary.md")
	outputPath := filepath.Join(root, "github-output")
	writeJSONForTest(t, metadataPath, testPullRequest())
	writeFileForTest(t, outputPath, "")

	runner := newGitAndValidatorRunner(root)
	runner.headSHA = testTagSHA
	err := runPlanCommand(context.Background(), []string{
		"-root", root,
		"-repository", "Azure/azure-cosmos-driver",
		"-version", "0.1.0",
		"-pr-metadata", metadataPath,
		"-validator", "trusted-validator",
		"-output", planPath,
		"-summary", summaryPath,
		"-github-output", outputPath,
	}, runner)
	if err == nil {
		t.Fatal("runPlanCommand() error = nil, want non-zero candidate HEAD mismatch")
	}

	contents, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var plan releasePlan
	if err := json.Unmarshal(contents, &plan); err != nil {
		t.Fatal(err)
	}
	wantReason := "candidate HEAD is " + testTagSHA + ", expected derived merge SHA " + testMergeSHA
	if plan.Status != "blocked" || len(plan.Reasons) != 1 || !strings.Contains(plan.Reasons[0], wantReason) {
		t.Fatalf("blocked plan = %#v, want reason containing %q", plan, wantReason)
	}

	summary, readErr := os.ReadFile(summaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(summary), "`blocked`") || !strings.Contains(string(summary), wantReason) {
		t.Fatalf("summary = %q, want blocked status and candidate HEAD mismatch", summary)
	}

	outputs, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(outputs), "status=blocked\n") {
		t.Fatalf("GitHub outputs = %q, want blocked status", outputs)
	}
}

func TestPublishReleaseCreatesSixAnnotatedTagsWithOneAtomicPush(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	approved := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	digest := mustPlanDigest(t, approved)
	runner := newGitAndValidatorRunner(root)
	tags := &sequenceTagReader{rounds: []map[string]tagResolution{
		absentTagResolutions("0.1.0"),
		targetTagResolutions("0.1.0"),
	}}

	result, err := publishRelease(context.Background(), publishOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
		Remote:        "origin",
	}, testPullRequest(), approved, digest, runner, tags)
	if err != nil {
		t.Fatalf("publishRelease() error = %v", err)
	}
	if result.Status != "published" || !result.PushAttempted || !result.AtomicPush {
		t.Fatalf("publishRelease() result = %#v", result)
	}
	if len(runner.tagCommands) != len(expectedModulePaths) {
		t.Fatalf("annotated tag commands = %d, want %d", len(runner.tagCommands), len(expectedModulePaths))
	}
	for index, command := range runner.tagCommands {
		modulePath := expectedModulePaths[index]
		tag := modulePath + "/v0.1.0"
		assertContainsArguments(t, command, "--annotate", "--no-sign", "--message", tag, testMergeSHA)
		message := command[4]
		for _, text := range []string{
			"Module: " + modulePath,
			"Version: 0.1.0",
			"Driver merge SHA: " + testMergeSHA,
			"Upstream source SHA: " + testSourceSHA,
			"Native interface version: 0.1.0",
			"Rust implementation version: 0.8.0",
		} {
			if !strings.Contains(message, text) {
				t.Fatalf("tag message %q does not contain %q", message, text)
			}
		}
	}
	if len(runner.pushCommands) != 1 {
		t.Fatalf("push commands = %v, want exactly one", runner.pushCommands)
	}
	wantPush := []string{"push", "--atomic", "origin"}
	for _, modulePath := range expectedModulePaths {
		ref := "refs/tags/" + modulePath + "/v0.1.0"
		wantPush = append(wantPush, ref+":"+ref)
	}
	if !reflect.DeepEqual(runner.pushCommands[0], wantPush) {
		t.Fatalf("atomic push argv = %v, want %v", runner.pushCommands[0], wantPush)
	}
}

func TestPublishReleaseAlreadyPublishedIsNoOp(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	targets := targetTagResolutions("0.1.0")
	approved := testReleasePlan("already_published", targets)
	runner := newGitAndValidatorRunner(root)
	tags := &sequenceTagReader{rounds: []map[string]tagResolution{targets, targets}}

	result, err := publishRelease(context.Background(), publishOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
		Remote:        "origin",
	}, testPullRequest(), approved, mustPlanDigest(t, approved), runner, tags)
	if err != nil {
		t.Fatalf("publishRelease() error = %v", err)
	}
	if result.Status != "already_published" || result.PushAttempted {
		t.Fatalf("publishRelease() result = %#v", result)
	}
	assertNoPublicationMutation(t, runner)
}

func TestPublishReleaseBlocksUnsafePreflightAndFinalStates(t *testing.T) {
	t.Parallel()
	partial := absentTagResolutions("0.1.0")
	partial[expectedModulePaths[0]+"/v0.1.0"] = targetTagResolutions("0.1.0")[expectedModulePaths[0]+"/v0.1.0"]
	mismatched := absentTagResolutions("0.1.0")
	mismatched[expectedModulePaths[0]+"/v0.1.0"] = tagResolution{
		Present:        true,
		Kind:           "lightweight",
		ReferenceSHA:   testTagSHA,
		ResolvedCommit: testTagSHA,
	}
	malformed := absentTagResolutions("0.1.0")
	malformed[expectedModulePaths[0]+"/v0.1.0"] = tagResolution{
		Present: true,
		Problem: "unsupported object",
	}

	for _, test := range []struct {
		name           string
		approved       releasePlan
		finalTags      map[string]tagResolution
		approvedDigest func(*testing.T, releasePlan) string
	}{
		{
			name:           "blocked preflight",
			approved:       testReleasePlan("blocked", partial),
			finalTags:      absentTagResolutions("0.1.0"),
			approvedDigest: mustPlanDigest,
		},
		{
			name:           "partial final tags",
			approved:       testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0")),
			finalTags:      partial,
			approvedDigest: mustPlanDigest,
		},
		{
			name:           "mismatched final tag",
			approved:       testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0")),
			finalTags:      mismatched,
			approvedDigest: mustPlanDigest,
		},
		{
			name:           "malformed final tag",
			approved:       testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0")),
			finalTags:      malformed,
			approvedDigest: mustPlanDigest,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := writeContractFixture(t, "0.1.0")
			runner := newGitAndValidatorRunner(root)
			tags := &sequenceTagReader{rounds: []map[string]tagResolution{test.finalTags}}
			result, err := publishRelease(context.Background(), publishOptions{
				Root:          root,
				Repository:    "Azure/azure-cosmos-driver",
				Version:       "0.1.0",
				ValidatorPath: "trusted-validator",
				Remote:        "origin",
			}, testPullRequest(), test.approved, test.approvedDigest(t, test.approved), runner, tags)
			if err == nil || result.Status != "blocked" {
				t.Fatalf("publishRelease() result = %#v, error = %v, want blocked", result, err)
			}
			assertNoPublicationMutation(t, runner)
		})
	}
}

func TestPublishReleaseBlocksApprovalDrift(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*releasePlan, *pullRequest, *publishOptions, *gitAndValidatorRunner)
		digest func(*testing.T, releasePlan) string
	}{
		{
			name:   "plan digest",
			mutate: func(_ *releasePlan, _ *pullRequest, _ *publishOptions, _ *gitAndValidatorRunner) {},
			digest: func(_ *testing.T, _ releasePlan) string { return strings.Repeat("0", 64) },
		},
		{
			name: "merge SHA",
			mutate: func(_ *releasePlan, pr *pullRequest, _ *publishOptions, _ *gitAndValidatorRunner) {
				pr.MergeCommitSHA = testTagSHA
			},
			digest: mustPlanDigest,
		},
		{
			name: "version",
			mutate: func(_ *releasePlan, _ *pullRequest, options *publishOptions, _ *gitAndValidatorRunner) {
				options.Version = "0.2.0"
			},
			digest: mustPlanDigest,
		},
		{
			name: "PR number",
			mutate: func(_ *releasePlan, pr *pullRequest, _ *publishOptions, _ *gitAndValidatorRunner) {
				pr.Number = 15
			},
			digest: mustPlanDigest,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := writeContractFixture(t, "0.1.0")
			approved := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
			pr := testPullRequest()
			options := publishOptions{
				Root:          root,
				Repository:    "Azure/azure-cosmos-driver",
				Version:       "0.1.0",
				ValidatorPath: "trusted-validator",
				Remote:        "origin",
			}
			runner := newGitAndValidatorRunner(root)
			test.mutate(&approved, &pr, &options, runner)
			tags := &sequenceTagReader{rounds: []map[string]tagResolution{
				absentTagResolutions("0.1.0"),
			}}
			result, err := publishRelease(
				context.Background(),
				options,
				pr,
				approved,
				test.digest(t, approved),
				runner,
				tags,
			)
			if err == nil || result.Status != "blocked" {
				t.Fatalf("publishRelease() result = %#v, error = %v, want blocked", result, err)
			}
			assertNoPublicationMutation(t, runner)
		})
	}
}

func TestPlanDigestBindsImmutableApprovalContract(t *testing.T) {
	t.Parallel()
	original := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	originalDigest := mustPlanDigest(t, original)
	for _, test := range []struct {
		name   string
		mutate func(*releasePlan)
	}{
		{
			name:   "source PR",
			mutate: func(plan *releasePlan) { plan.SourcePR++ },
		},
		{
			name:   "merge SHA",
			mutate: func(plan *releasePlan) { plan.DerivedMergeSHA = testTagSHA },
		},
		{
			name:   "version",
			mutate: func(plan *releasePlan) { plan.RequestedVersion = "0.2.0" },
		},
		{
			name:   "module path",
			mutate: func(plan *releasePlan) { plan.Modules[0].Path = "windows/arm64" },
		},
		{
			name:   "module tag",
			mutate: func(plan *releasePlan) { plan.Modules[0].Tag = "windows/amd64/v0.2.0" },
		},
		{
			name:   "module tag state",
			mutate: func(plan *releasePlan) { plan.Modules[0].TagState = "at_target" },
		},
		{
			name:   "module tag kind",
			mutate: func(plan *releasePlan) { plan.Modules[0].TagKind = "annotated" },
		},
		{
			name:   "module remote object",
			mutate: func(plan *releasePlan) { plan.Modules[0].RemoteObjectSHA = testTagSHA },
		},
		{
			name:   "module resolved commit",
			mutate: func(plan *releasePlan) { plan.Modules[0].ResolvedCommitSHA = testMergeSHA },
		},
		{
			name:   "plan status",
			mutate: func(plan *releasePlan) { plan.Status = "already_published" },
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := original
			changed.Modules = append([]modulePlan(nil), original.Modules...)
			test.mutate(&changed)
			if digest := mustPlanDigest(t, changed); digest == originalDigest {
				t.Fatalf("plan digest did not bind %s", test.name)
			}
		})
	}
}

func TestPlanDigestExcludesRemoteDefaultBranchTip(t *testing.T) {
	t.Parallel()
	original := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	advanced := original
	advanced.RemoteDefaultBranchSHA = "6666666666666666666666666666666666666666"
	if got, want := mustPlanDigest(t, advanced), mustPlanDigest(t, original); got != want {
		t.Fatalf("digest after default branch advance = %s, want %s", got, want)
	}
}

func TestPublishReleaseAllowsDefaultBranchAdvanceWhenMergeRemainsReachable(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	approved := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	runner := newGitAndValidatorRunner(root)
	runner.remoteBaseSHA = "6666666666666666666666666666666666666666"
	tags := &sequenceTagReader{rounds: []map[string]tagResolution{
		absentTagResolutions("0.1.0"),
		targetTagResolutions("0.1.0"),
	}}

	result, err := publishRelease(context.Background(), publishOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
		Remote:        "origin",
	}, testPullRequest(), approved, mustPlanDigest(t, approved), runner, tags)
	if err != nil {
		t.Fatalf("publishRelease() error = %v", err)
	}
	if result.Status != "published" || !result.PushAttempted {
		t.Fatalf("publishRelease() result = %#v", result)
	}
	if result.FinalPlanDigest != result.ApprovedPlanDigest {
		t.Fatalf(
			"final plan digest = %s, want approved digest %s",
			result.FinalPlanDigest,
			result.ApprovedPlanDigest,
		)
	}
	if result.ApprovedDefaultTipSHA != approved.RemoteDefaultBranchSHA ||
		result.FinalDefaultTipSHA != runner.remoteBaseSHA {
		t.Fatalf("publication default branch observations = %#v", result)
	}
}

func TestPublishReleaseBlocksWhenMergeIsNoLongerReachableAfterRefresh(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	approved := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	runner := newGitAndValidatorRunner(root)
	runner.remoteBaseSHA = "6666666666666666666666666666666666666666"
	runner.failGitCommand(
		"merge-base --is-ancestor "+testMergeSHA+" refs/remotes/origin/main",
		errors.New("merge is no longer reachable"),
	)
	tags := &sequenceTagReader{rounds: []map[string]tagResolution{
		absentTagResolutions("0.1.0"),
	}}

	result, err := publishRelease(context.Background(), publishOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
		Remote:        "origin",
	}, testPullRequest(), approved, mustPlanDigest(t, approved), runner, tags)
	if err == nil || result.Status != "blocked" ||
		!strings.Contains(strings.Join(result.Reasons, " "), "not reachable") {
		t.Fatalf("publishRelease() result = %#v, error = %v", result, err)
	}
	if result.FinalDefaultTipSHA != runner.remoteBaseSHA {
		t.Fatalf("publish-time default branch SHA = %q, want %q", result.FinalDefaultTipSHA, runner.remoteBaseSHA)
	}
	assertNoPublicationMutation(t, runner)
}

func TestPublishReleaseBlocksLocalTagCollisionBeforeMutation(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	approved := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	runner := newGitAndValidatorRunner(root)
	collidingTag := expectedModulePaths[2] + "/v0.1.0"
	runner.localRefs[collidingTag] = "refs/tags/" + collidingTag
	tags := &sequenceTagReader{rounds: []map[string]tagResolution{
		absentTagResolutions("0.1.0"),
	}}

	result, err := publishRelease(context.Background(), publishOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
		Remote:        "origin",
	}, testPullRequest(), approved, mustPlanDigest(t, approved), runner, tags)
	if err == nil || result.Status != "blocked" ||
		!strings.Contains(strings.Join(result.Reasons, " "), "local tag collision") {
		t.Fatalf("publishRelease() result = %#v, error = %v", result, err)
	}
	assertNoPublicationMutation(t, runner)
}

func TestPublishReleaseDoesNotFallBackAfterAtomicPushRejection(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	approved := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	runner := newGitAndValidatorRunner(root)
	runner.pushErr = errors.New("atomic push rejected")
	tags := &sequenceTagReader{rounds: []map[string]tagResolution{
		absentTagResolutions("0.1.0"),
		absentTagResolutions("0.1.0"),
	}}

	result, err := publishRelease(context.Background(), publishOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
		Remote:        "origin",
	}, testPullRequest(), approved, mustPlanDigest(t, approved), runner, tags)
	if err == nil || result.Status != "blocked" || !result.PushAttempted {
		t.Fatalf("publishRelease() result = %#v, error = %v", result, err)
	}
	if len(runner.pushCommands) != 1 || len(runner.pushCommands[0]) < 2 ||
		runner.pushCommands[0][1] != "--atomic" {
		t.Fatalf("push commands = %v, want one atomic attempt and no fallback", runner.pushCommands)
	}
}

func TestPublishReleaseReportsConcurrentWinnerAfterPushRejectionAsIndeterminate(t *testing.T) {
	t.Parallel()
	root := writeContractFixture(t, "0.1.0")
	approved := testReleasePlan("eligible_to_publish", absentTagResolutions("0.1.0"))
	runner := newGitAndValidatorRunner(root)
	runner.pushErr = errors.New("atomic push rejected")
	tags := &sequenceTagReader{rounds: []map[string]tagResolution{
		absentTagResolutions("0.1.0"),
		targetTagResolutions("0.1.0"),
	}}

	result, err := publishRelease(context.Background(), publishOptions{
		Root:          root,
		Repository:    "Azure/azure-cosmos-driver",
		Version:       "0.1.0",
		ValidatorPath: "trusted-validator",
		Remote:        "origin",
	}, testPullRequest(), approved, mustPlanDigest(t, approved), runner, tags)
	if err == nil || result.Status != "indeterminate" || !result.PushAttempted {
		t.Fatalf("publishRelease() result = %#v, error = %v", result, err)
	}
	if len(runner.pushCommands) != 1 {
		t.Fatalf("push commands = %v, want one atomic attempt and no fallback", runner.pushCommands)
	}
}

func TestPublishWorkflowIsolatedPermissionsAndEnvironment(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile(filepath.Join(
		repositoryRootForTest(t),
		".github",
		"workflows",
		"publish-generated-driver-release.yml",
	))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	for _, required := range []string{
		"workflow_dispatch:",
		"group: generated-driver-release",
		"cancel-in-progress: false",
		"environment: driver-release",
		"contents: read",
		"contents: write",
		"pull-requests: read",
		"persist-credentials: false",
		"persist-credentials: true",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("publication workflow is missing %q", required)
		}
	}
	if strings.Count(workflow, "contents: write") != 1 {
		t.Fatalf("publication workflow contents:write count = %d, want one isolated job", strings.Count(workflow, "contents: write"))
	}
	for _, forbidden := range []string{"pull_request:", "release:", "environment: production"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("publication workflow unexpectedly contains %q", forbidden)
		}
	}
}

func repositoryRootForTest(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
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

func targetTagResolutions(version string) map[string]tagResolution {
	resolutions := make(map[string]tagResolution, len(expectedModulePaths))
	for _, modulePath := range expectedModulePaths {
		tag := modulePath + "/v" + version
		resolutions[tag] = tagResolution{
			Present:        true,
			Kind:           "annotated",
			ReferenceSHA:   testTagSHA,
			ResolvedCommit: testMergeSHA,
		}
	}
	return resolutions
}

func testReleasePlan(status string, resolutions map[string]tagResolution) releasePlan {
	modules, classifiedStatus, reasons := classifyTags("0.1.0", testMergeSHA, resolutions)
	if status != "blocked" && classifiedStatus != status {
		panic(fmt.Sprintf("test plan status %q does not match tag status %q", status, classifiedStatus))
	}
	if status == "blocked" {
		classifiedStatus = status
	}
	return releasePlan{
		SchemaVersion:             planSchemaVersion,
		Repository:                "Azure/azure-cosmos-driver",
		SourcePR:                  14,
		BaseBranch:                "main",
		BaseSHA:                   testBaseSHA,
		DerivedMergeSHA:           testMergeSHA,
		RemoteDefaultBranchSHA:    "5555555555555555555555555555555555555555",
		IntegrityValidatorBaseRef: testBaseSHA,
		UpstreamSourceSHA:         testSourceSHA,
		RequestedVersion:          "0.1.0",
		NativeInterfaceVersion:    "0.1.0",
		RustDriverVersion:         "0.8.0",
		Modules:                   modules,
		Status:                    classifiedStatus,
		Reasons:                   reasons,
	}
}

func mustPlanDigest(t *testing.T, plan releasePlan) string {
	t.Helper()
	digest, err := planDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func assertContainsArguments(t *testing.T, arguments []string, required ...string) {
	t.Helper()
	for _, value := range required {
		if !containsString(arguments, value) {
			t.Fatalf("arguments %v do not contain %q", arguments, value)
		}
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func assertNoPublicationMutation(t *testing.T, runner *gitAndValidatorRunner) {
	t.Helper()
	if len(runner.tagCommands) != 0 || len(runner.pushCommands) != 0 {
		t.Fatalf(
			"publication mutation occurred: tag commands=%v push commands=%v",
			runner.tagCommands,
			runner.pushCommands,
		)
	}
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

type sequenceTagReader struct {
	rounds []map[string]tagResolution
	calls  int
}

func (reader *sequenceTagReader) Lookup(
	_ context.Context,
	_ string,
	tag string,
) (tagResolution, error) {
	round := reader.calls / len(expectedModulePaths)
	reader.calls++
	if round >= len(reader.rounds) {
		return tagResolution{}, fmt.Errorf("unexpected tag lookup round %d for %q", round, tag)
	}
	resolution, exists := reader.rounds[round][tag]
	if !exists {
		return tagResolution{}, fmt.Errorf("test tag resolution missing for %q", tag)
	}
	return resolution, nil
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
	headSHA            string
	mergeSHA           string
	baseSHA            string
	remoteBaseSHA      string
	gitFailures        map[string]error
	localRefs          map[string]string
	tagCommands        [][]string
	pushCommands       [][]string
	pushErr            error
	validatorArguments []string
}

func newGitAndValidatorRunner(root string) *gitAndValidatorRunner {
	return &gitAndValidatorRunner{
		root:          root,
		headSHA:       testMergeSHA,
		mergeSHA:      testMergeSHA,
		baseSHA:       testBaseSHA,
		remoteBaseSHA: "5555555555555555555555555555555555555555",
		gitFailures:   make(map[string]error),
		localRefs:     make(map[string]string),
	}
}

func (runner *gitAndValidatorRunner) failGitCommand(command string, err error) {
	runner.gitFailures[command] = err
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
	if err := runner.gitFailures[joined]; err != nil {
		return nil, err
	}
	switch {
	case len(gitArguments) == 4 &&
		gitArguments[0] == "fetch" &&
		gitArguments[1] == "--no-tags" &&
		gitArguments[2] == "origin":
		return nil, nil
	case joined == "rev-parse --verify HEAD^{commit}":
		return []byte(runner.headSHA + "\n"), nil
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
	case len(gitArguments) == 3 &&
		gitArguments[0] == "for-each-ref" &&
		gitArguments[1] == "--format=%(refname)":
		tag := strings.TrimPrefix(gitArguments[2], "refs/tags/")
		return []byte(runner.localRefs[tag]), nil
	case len(gitArguments) == 7 &&
		gitArguments[0] == "tag" &&
		gitArguments[1] == "--annotate" &&
		gitArguments[2] == "--no-sign" &&
		gitArguments[3] == "--message":
		runner.tagCommands = append(runner.tagCommands, append([]string(nil), gitArguments...))
		runner.localRefs[gitArguments[5]] = "refs/tags/" + gitArguments[5]
		return nil, nil
	case len(gitArguments) >= 3 && gitArguments[0] == "push":
		runner.pushCommands = append(runner.pushCommands, append([]string(nil), gitArguments...))
		return nil, runner.pushErr
	default:
		return nil, fmt.Errorf("unexpected git command: %s", joined)
	}
}
