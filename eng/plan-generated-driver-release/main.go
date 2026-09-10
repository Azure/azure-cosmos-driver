// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	moduleRoot        = "github.com/Azure/azure-cosmos-driver"
	planSchemaVersion = 1
)

var (
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repositoryPattern = regexp.MustCompile(
		`^[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?/[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?$`,
	)
	semverPattern        = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	digestPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	headerVersionPattern = regexp.MustCompile(
		`(?m)^[\t ]*#define[\t ]+AZURECOSMOSDRIVER_H_VERSION[\t ]+"([^"\r\n]+)"[\t ]*(?:[/][/*].*)?$`,
	)
	expectedModulePaths = []string{
		"windows/amd64",
		"linux/amd64",
		"linux/arm64",
		"linux/amd64-musl",
		"linux/arm64-musl",
		"darwin/arm64",
	}
)

type commandRunner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, directory, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed: %w", name, err)
	}
	return output, nil
}

type pullRequest struct {
	Number         int    `json:"number"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Base           struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

type provenance struct {
	SchemaVersion          int                `json:"schema_version"`
	SourceCommit           string             `json:"source_commit"`
	NativeInterfaceCrate   string             `json:"native_interface_crate"`
	NativeInterfaceVersion string             `json:"native_interface_version"`
	RustDriverCrate        string             `json:"rust_driver_crate"`
	RustDriverVersion      string             `json:"rust_driver_version"`
	Targets                []provenanceTarget `json:"targets"`
}

type provenanceTarget struct {
	ID                  string `json:"id"`
	Triple              string `json:"triple"`
	ModulePath          string `json:"module_path"`
	StaticLibrarySHA256 string `json:"static_library_sha256"`
	HeaderSHA256        string `json:"header_sha256"`
}

type gitObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type gitReference struct {
	Ref    string    `json:"ref"`
	Object gitObject `json:"object"`
}

type gitTag struct {
	Object gitObject `json:"object"`
}

type tagResolution struct {
	Present        bool
	Kind           string
	ReferenceSHA   string
	ResolvedCommit string
	Problem        string
}

type tagReader interface {
	Lookup(context.Context, string, string) (tagResolution, error)
}

type githubClient struct {
	runner commandRunner
}

type releasePlan struct {
	SchemaVersion             int          `json:"schema_version"`
	Repository                string       `json:"repository"`
	SourcePR                  int          `json:"source_pr"`
	BaseBranch                string       `json:"base_branch"`
	BaseSHA                   string       `json:"base_sha"`
	DerivedMergeSHA           string       `json:"derived_merge_sha"`
	RemoteDefaultBranchSHA    string       `json:"remote_default_branch_sha,omitempty"`
	IntegrityValidatorBaseRef string       `json:"integrity_validator_base_ref"`
	UpstreamSourceSHA         string       `json:"upstream_source_sha,omitempty"`
	RequestedVersion          string       `json:"requested_version"`
	NativeInterfaceVersion    string       `json:"native_interface_version,omitempty"`
	RustDriverVersion         string       `json:"rust_driver_version,omitempty"`
	Modules                   []modulePlan `json:"modules"`
	Status                    string       `json:"status"`
	Reasons                   []string     `json:"reasons"`
}

type modulePlan struct {
	Path              string `json:"path"`
	Tag               string `json:"tag"`
	TagState          string `json:"tag_state"`
	TagKind           string `json:"tag_kind,omitempty"`
	RemoteObjectSHA   string `json:"remote_object_sha,omitempty"`
	ResolvedCommitSHA string `json:"resolved_commit_sha,omitempty"`
}

type planOptions struct {
	Root          string
	Repository    string
	Version       string
	ValidatorPath string
}

type publicationResult struct {
	SchemaVersion          int          `json:"schema_version"`
	Repository             string       `json:"repository"`
	SourcePR               int          `json:"source_pr"`
	DerivedMergeSHA        string       `json:"derived_merge_sha"`
	RequestedVersion       string       `json:"requested_version"`
	UpstreamSourceSHA      string       `json:"upstream_source_sha,omitempty"`
	NativeInterfaceVersion string       `json:"native_interface_version,omitempty"`
	RustDriverVersion      string       `json:"rust_driver_version,omitempty"`
	ApprovedPlanDigest     string       `json:"approved_plan_digest"`
	FinalPlanDigest        string       `json:"final_plan_digest,omitempty"`
	Modules                []modulePlan `json:"modules"`
	Status                 string       `json:"status"`
	PushAttempted          bool         `json:"push_attempted"`
	AtomicPush             bool         `json:"atomic_push"`
	Reasons                []string     `json:"reasons"`
}

type publishOptions struct {
	Root          string
	Repository    string
	Version       string
	ValidatorPath string
	Remote        string
}

func main() {
	if len(os.Args) < 2 {
		exitError(errors.New("usage: release-planner <resolve-pr|plan|publish> [options]"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch os.Args[1] {
	case "resolve-pr":
		exitError(runResolvePR(ctx, os.Args[2:], execCommandRunner{}))
	case "plan":
		exitError(runPlanCommand(ctx, os.Args[2:], execCommandRunner{}))
	case "publish":
		exitError(runPublishCommand(ctx, os.Args[2:], execCommandRunner{}))
	default:
		exitError(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}

func exitError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "release planning failed: %v\n", err)
	os.Exit(1)
}

func runResolvePR(ctx context.Context, arguments []string, runner commandRunner) error {
	flags := flag.NewFlagSet("resolve-pr", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repository := flags.String("repository", "", "owner/repository")
	prNumber := flags.Int("pr-number", 0, "merged generated-driver pull request number")
	defaultBranch := flags.String("default-branch", "", "repository default branch")
	version := flags.String("version", "", "requested release version")
	output := flags.String("output", "", "path for validated pull request metadata")
	planOutput := flags.String("plan-output", "", "fallback blocked plan JSON")
	summary := flags.String("summary", "", "GitHub Step Summary file")
	githubOutput := flags.String("github-output", "", "GitHub Actions output file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("-output is required")
	}
	pr := pullRequest{Number: *prNumber}
	pr.Base.Ref = *defaultBranch
	fail := func(cause error) error {
		plan := newReleasePlan(*repository, *version, pr)
		plan.Reasons = []string{cause.Error()}
		if *planOutput != "" {
			if artifactErr := writePlanArtifacts(plan, *planOutput, *summary, *githubOutput); artifactErr != nil {
				return errors.Join(cause, artifactErr)
			}
		}
		return cause
	}
	if err := validateRepositoryName(*repository); err != nil {
		return fail(err)
	}
	if *prNumber <= 0 {
		return fail(errors.New("-pr-number must be a positive integer"))
	}
	if err := validateBranchName(*defaultBranch); err != nil {
		return fail(fmt.Errorf("invalid repository default branch: %w", err))
	}

	client := githubClient{runner: runner}
	pr, err := client.PullRequest(ctx, *repository, *prNumber)
	if err != nil {
		return fail(err)
	}
	if err := validatePullRequest(pr, *repository, *prNumber, *defaultBranch); err != nil {
		return fail(err)
	}
	if err := writeJSON(*output, pr); err != nil {
		return err
	}
	if *githubOutput != "" {
		outputs := map[string]string{
			"base_branch": pr.Base.Ref,
			"base_sha":    pr.Base.SHA,
			"merge_sha":   pr.MergeCommitSHA,
			"pr_number":   strconv.Itoa(pr.Number),
		}
		if err := appendGitHubOutputs(*githubOutput, outputs); err != nil {
			return err
		}
	}
	fmt.Printf("Resolved merged pull request #%d to %s.\n", pr.Number, pr.MergeCommitSHA)
	return nil
}

func runPlanCommand(ctx context.Context, arguments []string, runner commandRunner) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "checked-out merged candidate repository")
	repository := flags.String("repository", "", "owner/repository")
	version := flags.String("version", "", "canonical stable module version without leading v")
	metadata := flags.String("pr-metadata", "", "validated pull request metadata JSON")
	validator := flags.String("validator", "", "trusted generated-driver validator executable")
	output := flags.String("output", "", "machine-readable release plan JSON")
	summary := flags.String("summary", "", "GitHub Step Summary file")
	githubOutput := flags.String("github-output", "", "GitHub Actions output file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *root == "" || *metadata == "" || *validator == "" || *output == "" {
		return errors.New("-root, -pr-metadata, -validator, and -output are required")
	}

	pr, err := readPullRequest(*metadata)
	if err != nil {
		return err
	}
	plan := newReleasePlan(*repository, *version, pr)
	client := githubClient{runner: runner}
	plan, planErr := buildReleasePlan(ctx, planOptions{
		Root:          *root,
		Repository:    *repository,
		Version:       *version,
		ValidatorPath: *validator,
	}, pr, runner, client)
	if err := writePlanArtifacts(plan, *output, *summary, *githubOutput); err != nil {
		return err
	}
	if planErr != nil {
		return planErr
	}
	fmt.Printf("Generated release plan with status %s.\n", plan.Status)
	return nil
}

func runPublishCommand(ctx context.Context, arguments []string, runner commandRunner) error {
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "checked-out merged candidate repository")
	repository := flags.String("repository", "", "owner/repository")
	version := flags.String("version", "", "canonical stable module version without leading v")
	metadata := flags.String("pr-metadata", "", "freshly validated pull request metadata JSON")
	validator := flags.String("validator", "", "trusted generated-driver validator executable")
	approvedPlanPath := flags.String("approved-plan", "", "pre-approval release plan JSON")
	approvedDigest := flags.String("approved-plan-digest", "", "SHA-256 digest of the approved plan")
	output := flags.String("output", "", "machine-readable publication result JSON")
	summary := flags.String("summary", "", "GitHub Step Summary file")
	githubOutput := flags.String("github-output", "", "GitHub Actions output file")
	remote := flags.String("remote", "origin", "authenticated Git remote used for the atomic push")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *root == "" ||
		*repository == "" ||
		*metadata == "" ||
		*validator == "" ||
		*approvedPlanPath == "" ||
		*output == "" {
		return errors.New(
			"-root, -repository, -pr-metadata, -validator, -approved-plan, and -output are required",
		)
	}

	pr, err := readPullRequest(*metadata)
	if err != nil {
		return err
	}
	approvedPlan, err := readReleasePlan(*approvedPlanPath)
	if err != nil {
		return err
	}
	result := newPublicationResult(*repository, *version, *approvedDigest, approvedPlan)
	client := githubClient{runner: runner}
	result, publishErr := publishRelease(ctx, publishOptions{
		Root:          *root,
		Repository:    *repository,
		Version:       *version,
		ValidatorPath: *validator,
		Remote:        *remote,
	}, pr, approvedPlan, *approvedDigest, runner, client)
	if err := writePublicationArtifacts(result, *output, *summary, *githubOutput); err != nil {
		return err
	}
	if publishErr != nil {
		return publishErr
	}
	fmt.Printf("Generated publication result with status %s.\n", result.Status)
	return nil
}

func newPublicationResult(
	repository string,
	version string,
	approvedDigest string,
	approvedPlan releasePlan,
) publicationResult {
	return publicationResult{
		SchemaVersion:          planSchemaVersion,
		Repository:             repository,
		SourcePR:               approvedPlan.SourcePR,
		DerivedMergeSHA:        approvedPlan.DerivedMergeSHA,
		RequestedVersion:       version,
		UpstreamSourceSHA:      approvedPlan.UpstreamSourceSHA,
		NativeInterfaceVersion: approvedPlan.NativeInterfaceVersion,
		RustDriverVersion:      approvedPlan.RustDriverVersion,
		ApprovedPlanDigest:     approvedDigest,
		Modules:                append([]modulePlan(nil), approvedPlan.Modules...),
		Status:                 "blocked",
		AtomicPush:             true,
		Reasons:                []string{"publication did not complete"},
	}
}

func publishRelease(
	ctx context.Context,
	options publishOptions,
	pr pullRequest,
	approvedPlan releasePlan,
	approvedDigest string,
	runner commandRunner,
	tags tagReader,
) (publicationResult, error) {
	result := newPublicationResult(options.Repository, options.Version, approvedDigest, approvedPlan)
	fail := func(status, reason string, err error) (publicationResult, error) {
		result.Status = status
		result.Reasons = []string{fmt.Sprintf("%s: %v", reason, err)}
		return result, fmt.Errorf("%s: %w", reason, err)
	}

	if options.Remote != "origin" {
		return fail("blocked", "publication remote is invalid", errors.New(`only "origin" is allowed`))
	}
	if err := validateApprovedPlan(approvedPlan, options, pr); err != nil {
		return fail("blocked", "approved plan is invalid", err)
	}
	actualApprovedDigest, err := planDigest(approvedPlan)
	if err != nil {
		return fail("blocked", "approved plan digest failed", err)
	}
	if !digestPattern.MatchString(approvedDigest) || actualApprovedDigest != approvedDigest {
		return fail(
			"blocked",
			"approved plan digest does not match",
			fmt.Errorf("expected %s, got %s", actualApprovedDigest, approvedDigest),
		)
	}
	if err := refreshRemoteDefaultBranch(ctx, options.Root, pr.Base.Ref, options.Remote, runner); err != nil {
		return fail("blocked", "final default-branch refresh failed", err)
	}

	finalPlan, planErr := buildReleasePlan(ctx, planOptions{
		Root:          options.Root,
		Repository:    options.Repository,
		Version:       options.Version,
		ValidatorPath: options.ValidatorPath,
	}, pr, runner, tags)
	result.Modules = append([]modulePlan(nil), finalPlan.Modules...)
	result.UpstreamSourceSHA = finalPlan.UpstreamSourceSHA
	result.NativeInterfaceVersion = finalPlan.NativeInterfaceVersion
	result.RustDriverVersion = finalPlan.RustDriverVersion
	if planErr != nil {
		return fail("blocked", "final release revalidation failed", planErr)
	}
	finalDigest, err := planDigest(finalPlan)
	if err != nil {
		return fail("blocked", "final plan digest failed", err)
	}
	result.FinalPlanDigest = finalDigest
	if finalDigest != approvedDigest {
		return fail(
			"blocked",
			"release plan drifted while awaiting approval",
			fmt.Errorf("approved=%s final=%s", approvedDigest, finalDigest),
		)
	}

	if finalPlan.Status == "already_published" {
		verifiedModules, verifyStatus, reasons, err := verifyRemoteTags(
			ctx,
			options.Repository,
			options.Version,
			pr.MergeCommitSHA,
			tags,
		)
		result.Modules = verifiedModules
		if err != nil {
			result.Status = "indeterminate"
			result.Reasons = []string{fmt.Sprintf("idempotency verification failed: %v", err)}
			return result, err
		}
		if verifyStatus != "already_published" {
			result.Status = "blocked"
			result.Reasons = append(
				[]string{"remote tags changed during idempotency verification"},
				reasons...,
			)
			return result, errors.New(strings.Join(result.Reasons, "; "))
		}
		result.Status = "already_published"
		result.Reasons = reasons
		return result, nil
	}
	if finalPlan.Status != "eligible_to_publish" {
		return fail(
			"blocked",
			"approved plan is not publishable",
			fmt.Errorf("unexpected status %q", finalPlan.Status),
		)
	}

	for _, module := range finalPlan.Modules {
		existing, err := localTagReference(ctx, options.Root, module.Tag, runner)
		if err != nil {
			return fail("blocked", "local tag collision check failed", err)
		}
		if existing != "" {
			return fail(
				"blocked",
				"local tag collision detected",
				fmt.Errorf("%s already exists as %s", module.Tag, existing),
			)
		}
	}
	for _, module := range finalPlan.Modules {
		message := annotatedTagMessage(module.Path, finalPlan)
		if _, err := runGit(
			ctx,
			runner,
			options.Root,
			"tag",
			"--annotate",
			"--no-sign",
			"--message",
			message,
			module.Tag,
			pr.MergeCommitSHA,
		); err != nil {
			return fail("blocked", "local annotated tag creation failed", err)
		}
	}

	pushArguments := []string{"push", "--atomic", options.Remote}
	for _, module := range finalPlan.Modules {
		ref := "refs/tags/" + module.Tag
		pushArguments = append(pushArguments, ref+":"+ref)
	}
	result.PushAttempted = true
	_, pushErr := runGit(ctx, runner, options.Root, pushArguments...)

	verifiedModules, verifyStatus, reasons, verifyErr := verifyRemoteTags(
		ctx,
		options.Repository,
		options.Version,
		pr.MergeCommitSHA,
		tags,
	)
	result.Modules = verifiedModules
	if verifyErr != nil {
		result.Status = "indeterminate"
		result.Reasons = []string{fmt.Sprintf("post-publish verification failed: %v", verifyErr)}
		return result, errors.New(result.Reasons[0])
	}
	if pushErr != nil {
		if verifyStatus == "eligible_to_publish" {
			result.Status = "blocked"
			result.Reasons = []string{
				fmt.Sprintf("atomic push was rejected and remote verification found all six tags absent: %v", pushErr),
			}
			return result, errors.New(result.Reasons[0])
		}
		result.Status = "indeterminate"
		result.Reasons = append(
			[]string{fmt.Sprintf("atomic push was rejected but the remote tag state changed: %v", pushErr)},
			reasons...,
		)
		return result, errors.New(strings.Join(result.Reasons, "; "))
	}
	if verifyStatus == "already_published" {
		result.Status = "published"
		result.Reasons = []string{"all six published tags resolve to the derived merge SHA"}
		return result, nil
	}
	result.Status = "indeterminate"
	result.Reasons = append(
		[]string{"atomic push reported success but the expected remote tag state could not be verified"},
		reasons...,
	)
	return result, errors.New(strings.Join(result.Reasons, "; "))
}

func validateApprovedPlan(approved releasePlan, options publishOptions, pr pullRequest) error {
	if approved.SchemaVersion != planSchemaVersion {
		return fmt.Errorf("unsupported approved plan schema_version %d", approved.SchemaVersion)
	}
	if approved.Status != "eligible_to_publish" && approved.Status != "already_published" {
		return fmt.Errorf("approved plan status %q cannot be published", approved.Status)
	}
	if approved.Repository != options.Repository ||
		approved.SourcePR != pr.Number ||
		approved.BaseBranch != pr.Base.Ref ||
		approved.BaseSHA != pr.Base.SHA ||
		approved.DerivedMergeSHA != pr.MergeCommitSHA ||
		approved.RequestedVersion != options.Version {
		return errors.New("approved plan repository, PR, merge, base, or version does not match fresh inputs")
	}
	if len(approved.Modules) != len(expectedModulePaths) {
		return fmt.Errorf("approved plan contains %d modules; expected six", len(approved.Modules))
	}
	for index, modulePath := range expectedModulePaths {
		module := approved.Modules[index]
		if module.Path != modulePath || module.Tag != modulePath+"/v"+options.Version {
			return fmt.Errorf("approved plan module %d does not match the lockstep tag contract", index)
		}
	}
	return nil
}

func refreshRemoteDefaultBranch(
	ctx context.Context,
	root string,
	defaultBranch string,
	remote string,
	runner commandRunner,
) error {
	if err := validateBranchName(defaultBranch); err != nil {
		return err
	}
	refspec := "+refs/heads/" + defaultBranch + ":refs/remotes/origin/" + defaultBranch
	if _, err := runGit(ctx, runner, root, "fetch", "--no-tags", remote, refspec); err != nil {
		return err
	}
	return nil
}

func localTagReference(ctx context.Context, root, tag string, runner commandRunner) (string, error) {
	expectedRef := "refs/tags/" + tag
	output, err := runGit(
		ctx,
		runner,
		root,
		"for-each-ref",
		"--format=%(refname)",
		expectedRef,
	)
	if err != nil {
		return "", err
	}
	if output == "" {
		return "", nil
	}
	if output != expectedRef {
		return "", fmt.Errorf("unexpected local reference output %q", output)
	}
	return output, nil
}

func annotatedTagMessage(modulePath string, plan releasePlan) string {
	return fmt.Sprintf(
		"Azure Cosmos DB generated driver module release\n\n"+
			"Module: %s\n"+
			"Version: %s\n"+
			"Driver merge SHA: %s\n"+
			"Upstream source SHA: %s\n"+
			"Native interface version: %s\n"+
			"Rust implementation version: %s",
		modulePath,
		plan.RequestedVersion,
		plan.DerivedMergeSHA,
		plan.UpstreamSourceSHA,
		plan.NativeInterfaceVersion,
		plan.RustDriverVersion,
	)
}

func verifyRemoteTags(
	ctx context.Context,
	repository string,
	version string,
	expectedCommit string,
	tags tagReader,
) ([]modulePlan, string, []string, error) {
	resolutions := make(map[string]tagResolution, len(expectedModulePaths))
	for _, modulePath := range expectedModulePaths {
		tag := modulePath + "/v" + version
		resolution, err := tags.Lookup(ctx, repository, tag)
		if err != nil {
			return nil, "indeterminate", nil, fmt.Errorf("verify remote tag %q: %w", tag, err)
		}
		resolutions[tag] = resolution
	}
	modules, status, reasons := classifyTags(version, expectedCommit, resolutions)
	return modules, status, reasons, nil
}

func newReleasePlan(repository, version string, pr pullRequest) releasePlan {
	modules := make([]modulePlan, 0, len(expectedModulePaths))
	for _, modulePath := range expectedModulePaths {
		modules = append(modules, modulePlan{
			Path:     modulePath,
			Tag:      modulePath + "/v" + version,
			TagState: "not_inspected",
		})
	}
	return releasePlan{
		SchemaVersion:    planSchemaVersion,
		Repository:       repository,
		SourcePR:         pr.Number,
		BaseBranch:       pr.Base.Ref,
		BaseSHA:          pr.Base.SHA,
		DerivedMergeSHA:  pr.MergeCommitSHA,
		RequestedVersion: version,
		Modules:          modules,
		Status:           "blocked",
		Reasons:          []string{"release planning did not complete"},
	}
}

func buildReleasePlan(
	ctx context.Context,
	options planOptions,
	pr pullRequest,
	runner commandRunner,
	tags tagReader,
) (releasePlan, error) {
	plan := newReleasePlan(options.Repository, options.Version, pr)
	block := func(reason string, err error) (releasePlan, error) {
		plan.Status = "blocked"
		plan.Reasons = []string{fmt.Sprintf("%s: %v", reason, err)}
		return plan, fmt.Errorf("%s: %w", reason, err)
	}

	if err := validateRepositoryName(options.Repository); err != nil {
		return block("repository identity is invalid", err)
	}
	if err := validatePullRequest(pr, options.Repository, pr.Number, pr.Base.Ref); err != nil {
		return block("pull request metadata is invalid", err)
	}
	if _, err := validateVersion(options.Version); err != nil {
		return block("requested version is invalid", err)
	}
	remoteDefaultBranchSHA, err := validateCandidateGitState(ctx, options.Root, pr, runner)
	if err != nil {
		return block("candidate merge commit validation failed", err)
	}
	plan.RemoteDefaultBranchSHA = remoteDefaultBranchSHA
	plan.IntegrityValidatorBaseRef = pr.Base.SHA
	if _, err := runner.Run(
		ctx,
		"",
		options.ValidatorPath,
		"integrity",
		"-root",
		options.Root,
		"-base-ref",
		plan.IntegrityValidatorBaseRef,
	); err != nil {
		return block("generated driver integrity validation failed", err)
	}

	manifest, contractErr := validateReleaseContract(options.Root, options.Version)
	plan.UpstreamSourceSHA = manifest.SourceCommit
	plan.NativeInterfaceVersion = manifest.NativeInterfaceVersion
	plan.RustDriverVersion = manifest.RustDriverVersion
	if contractErr != nil {
		return block("generated release contract validation failed", contractErr)
	}

	resolutions := make(map[string]tagResolution, len(expectedModulePaths))
	for index, modulePath := range expectedModulePaths {
		tag := modulePath + "/v" + options.Version
		resolution, err := tags.Lookup(ctx, options.Repository, tag)
		if err != nil {
			plan.Modules[index].TagState = "inspection_failed"
			return block("remote tag inspection failed", err)
		}
		resolutions[tag] = resolution
		plan.Modules[index] = describeTag(modulePath, tag, pr.MergeCommitSHA, resolution)
	}
	plan.Modules, plan.Status, plan.Reasons = classifyTags(options.Version, pr.MergeCommitSHA, resolutions)
	if plan.Status == "blocked" {
		return plan, errors.New(strings.Join(plan.Reasons, "; "))
	}
	return plan, nil
}

func validateRepositoryName(repository string) error {
	if !repositoryPattern.MatchString(repository) {
		return fmt.Errorf("repository must be an owner/name pair, got %q", repository)
	}
	return nil
}

func validateBranchName(branch string) error {
	if branch == "" ||
		strings.HasPrefix(branch, "-") ||
		strings.Contains(branch, "..") ||
		strings.Contains(branch, `\`) ||
		strings.Contains(branch, "@{") ||
		strings.HasSuffix(branch, ".") ||
		strings.HasSuffix(branch, "/") ||
		strings.ContainsAny(branch, " ~^:?*[") {
		return fmt.Errorf("unsafe branch name %q", branch)
	}
	return nil
}

func validatePullRequest(pr pullRequest, repository string, number int, defaultBranch string) error {
	if pr.Number != number {
		return fmt.Errorf("GitHub returned pull request #%d, expected #%d", pr.Number, number)
	}
	if !strings.EqualFold(pr.Base.Repo.FullName, repository) {
		return fmt.Errorf("pull request base repository is %q, expected %q", pr.Base.Repo.FullName, repository)
	}
	if !pr.Merged {
		return fmt.Errorf("pull request #%d is not merged", pr.Number)
	}
	if pr.Base.Ref != defaultBranch {
		return fmt.Errorf("pull request base branch is %q, expected repository default branch %q", pr.Base.Ref, defaultBranch)
	}
	if !commitPattern.MatchString(pr.Base.SHA) {
		return errors.New("pull request base SHA must be a lowercase 40-character Git commit")
	}
	if !commitPattern.MatchString(pr.MergeCommitSHA) {
		return errors.New("pull request merge commit SHA must be a lowercase 40-character Git commit")
	}
	return nil
}

func validateVersion(version string) (uint64, error) {
	match := semverPattern.FindStringSubmatch(version)
	if match == nil {
		return 0, fmt.Errorf("%q is not canonical stable SemVer X.Y.Z without a leading v", version)
	}
	major, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse SemVer major: %w", err)
	}
	if major >= 2 {
		return 0, errors.New("major versions 2 and later require /vN module path suffixes")
	}
	return major, nil
}

func validateCandidateGitState(
	ctx context.Context,
	root string,
	pr pullRequest,
	runner commandRunner,
) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve candidate root: %w", err)
	}
	if err := validateBranchName(pr.Base.Ref); err != nil {
		return "", err
	}
	head, err := runGit(ctx, runner, absoluteRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	if head != pr.MergeCommitSHA {
		return "", fmt.Errorf("candidate HEAD is %s, expected derived merge SHA %s", head, pr.MergeCommitSHA)
	}
	if _, err := runGit(ctx, runner, absoluteRoot, "cat-file", "-e", pr.MergeCommitSHA+"^{commit}"); err != nil {
		return "", fmt.Errorf("derived merge SHA is not a commit: %w", err)
	}
	if _, err := runGit(ctx, runner, absoluteRoot, "cat-file", "-e", pr.Base.SHA+"^{commit}"); err != nil {
		return "", fmt.Errorf("pull request base SHA is not a commit: %w", err)
	}
	if _, err := runGit(ctx, runner, absoluteRoot, "merge-base", "--is-ancestor", pr.Base.SHA, pr.MergeCommitSHA); err != nil {
		return "", errors.New("pull request base SHA is not an ancestor of the derived merge commit")
	}
	remoteBase := "refs/remotes/origin/" + pr.Base.Ref
	remoteDefaultBranchSHA, err := runGit(ctx, runner, absoluteRoot, "rev-parse", "--verify", remoteBase+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("current remote default branch is unavailable: %w", err)
	}
	if _, err := runGit(ctx, runner, absoluteRoot, "merge-base", "--is-ancestor", pr.MergeCommitSHA, remoteBase); err != nil {
		return "", fmt.Errorf("derived merge SHA is not reachable from current remote default branch %q", pr.Base.Ref)
	}
	return remoteDefaultBranchSHA, nil
}

func runGit(
	ctx context.Context,
	runner commandRunner,
	root string,
	arguments ...string,
) (string, error) {
	commandArguments := append([]string{"-C", root}, arguments...)
	output, err := runner.Run(ctx, "", "git", commandArguments...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func validateReleaseContract(root, requestedVersion string) (provenance, error) {
	contents, err := os.ReadFile(filepath.Join(root, "provenance.json"))
	if err != nil {
		return provenance{}, fmt.Errorf("read provenance.json: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest provenance
	if err := decoder.Decode(&manifest); err != nil {
		return provenance{}, fmt.Errorf("parse provenance.json: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return provenance{}, fmt.Errorf("parse provenance.json: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return manifest, fmt.Errorf("unsupported provenance schema_version %d", manifest.SchemaVersion)
	}
	if !commitPattern.MatchString(manifest.SourceCommit) {
		return manifest, errors.New("provenance source_commit must be a lowercase 40-character Git commit")
	}
	if _, err := validateVersion(manifest.NativeInterfaceVersion); err != nil {
		return manifest, fmt.Errorf("native interface version: %w", err)
	}
	if manifest.NativeInterfaceVersion != requestedVersion {
		return manifest, fmt.Errorf(
			"requested version %q does not match native interface version %q",
			requestedVersion,
			manifest.NativeInterfaceVersion,
		)
	}

	actualModules := make(map[string]int, len(manifest.Targets))
	for _, target := range manifest.Targets {
		actualModules[target.ModulePath]++
	}
	var missing, extra, duplicate []string
	expected := make(map[string]struct{}, len(expectedModulePaths))
	for _, modulePath := range expectedModulePaths {
		expected[modulePath] = struct{}{}
		switch actualModules[modulePath] {
		case 0:
			missing = append(missing, modulePath)
		case 1:
		default:
			duplicate = append(duplicate, modulePath)
		}
	}
	for modulePath := range actualModules {
		if _, exists := expected[modulePath]; !exists {
			extra = append(extra, modulePath)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(duplicate)
	if len(missing) != 0 || len(extra) != 0 || len(duplicate) != 0 {
		return manifest, fmt.Errorf(
			"expected exactly six lockstep modules (missing=%v extra=%v duplicate=%v)",
			missing,
			extra,
			duplicate,
		)
	}

	for _, modulePath := range expectedModulePaths {
		for _, relativeHeader := range []string{"azurecosmosdriver.h", filepath.Join("native", "azurecosmosdriver.h")} {
			filename := filepath.Join(root, filepath.FromSlash(modulePath), relativeHeader)
			headerVersion, err := readHeaderVersion(filename)
			if err != nil {
				return manifest, err
			}
			if headerVersion != manifest.NativeInterfaceVersion {
				return manifest, fmt.Errorf(
					"header %q version %q does not match native interface version %q",
					filepath.ToSlash(filename),
					headerVersion,
					manifest.NativeInterfaceVersion,
				)
			}
		}
	}
	return manifest, nil
}

func readHeaderVersion(filename string) (string, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("read native header %q: %w", filepath.ToSlash(filename), err)
	}
	matches := headerVersionPattern.FindAllSubmatch(contents, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf(
			"native header %q must define AZURECOSMOSDRIVER_H_VERSION exactly once",
			filepath.ToSlash(filename),
		)
	}
	version := string(matches[0][1])
	if _, err := validateVersion(version); err != nil {
		return "", fmt.Errorf("native header %q version: %w", filepath.ToSlash(filename), err)
	}
	return version, nil
}

func classifyTags(
	version string,
	expectedCommit string,
	resolutions map[string]tagResolution,
) ([]modulePlan, string, []string) {
	modules := make([]modulePlan, 0, len(expectedModulePaths))
	absentCount := 0
	targetCount := 0
	var reasons []string
	for _, modulePath := range expectedModulePaths {
		tag := modulePath + "/v" + version
		resolution, exists := resolutions[tag]
		entry := describeTag(modulePath, tag, expectedCommit, resolution)
		switch {
		case !exists:
			entry.TagState = "malformed"
			reasons = append(reasons, fmt.Sprintf("tag %s was not inspected", tag))
		case resolution.Problem != "":
			reasons = append(reasons, fmt.Sprintf("tag %s is malformed: %s", tag, resolution.Problem))
		case !resolution.Present:
			absentCount++
		case resolution.ResolvedCommit == expectedCommit:
			targetCount++
		default:
			reasons = append(reasons, fmt.Sprintf("tag %s does not resolve to the derived merge SHA", tag))
		}
		modules = append(modules, entry)
	}

	switch {
	case absentCount == len(expectedModulePaths):
		return modules, "eligible_to_publish", []string{"all six proposed tags are absent"}
	case targetCount == len(expectedModulePaths):
		return modules, "already_published", []string{"all six proposed tags already resolve to the derived merge SHA; publication is idempotent"}
	default:
		if absentCount > 0 && targetCount > 0 {
			reasons = append(reasons, "only a subset of the six lockstep tags is present at the derived merge SHA")
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "the six lockstep tags are not in a publishable or idempotent state")
		}
		sort.Strings(reasons)
		return modules, "blocked", reasons
	}
}

func describeTag(modulePath, tag, expectedCommit string, resolution tagResolution) modulePlan {
	entry := modulePlan{
		Path:              modulePath,
		Tag:               tag,
		TagKind:           resolution.Kind,
		RemoteObjectSHA:   resolution.ReferenceSHA,
		ResolvedCommitSHA: resolution.ResolvedCommit,
	}
	switch {
	case resolution.Problem != "":
		entry.TagState = "malformed"
	case !resolution.Present:
		entry.TagState = "absent"
	case resolution.ResolvedCommit == expectedCommit:
		entry.TagState = "at_target"
	default:
		entry.TagState = "wrong_target"
	}
	return entry
}

func (client githubClient) PullRequest(ctx context.Context, repository string, number int) (pullRequest, error) {
	endpoint := fmt.Sprintf("repos/%s/pulls/%d", repository, number)
	output, err := client.runner.Run(ctx, "", "gh", "api", "--method", "GET", endpoint)
	if err != nil {
		return pullRequest{}, fmt.Errorf("read pull request metadata: %w", err)
	}
	var pr pullRequest
	if err := json.Unmarshal(output, &pr); err != nil {
		return pullRequest{}, fmt.Errorf("parse pull request metadata: %w", err)
	}
	return pr, nil
}

func (client githubClient) Lookup(ctx context.Context, repository, tag string) (tagResolution, error) {
	endpoint := fmt.Sprintf("repos/%s/git/matching-refs/tags/%s", repository, tag)
	output, err := client.runner.Run(ctx, "", "gh", "api", "--method", "GET", endpoint)
	if err != nil {
		return tagResolution{}, fmt.Errorf("read tag %q: %w", tag, err)
	}
	var references []gitReference
	if err := json.Unmarshal(output, &references); err != nil {
		return tagResolution{}, fmt.Errorf("parse tag %q response: %w", tag, err)
	}
	expectedRef := "refs/tags/" + tag
	var exact []gitReference
	for _, reference := range references {
		if reference.Ref == expectedRef {
			exact = append(exact, reference)
		}
	}
	if len(exact) == 0 {
		return tagResolution{}, nil
	}
	if len(exact) != 1 {
		return tagResolution{
			Present: true,
			Problem: "GitHub returned multiple exact references",
		}, nil
	}

	reference := exact[0]
	resolution := tagResolution{
		Present:      true,
		Kind:         "lightweight",
		ReferenceSHA: reference.Object.SHA,
	}
	if !commitPattern.MatchString(reference.Object.SHA) {
		resolution.Problem = "reference object SHA is not a full lowercase Git object ID"
		return resolution, nil
	}
	switch reference.Object.Type {
	case "commit":
		resolution.ResolvedCommit = reference.Object.SHA
		return resolution, nil
	case "tag":
		resolution.Kind = "annotated"
	default:
		resolution.Problem = fmt.Sprintf("reference points to unsupported Git object type %q", reference.Object.Type)
		return resolution, nil
	}

	current := reference.Object
	seen := make(map[string]struct{})
	for depth := 0; depth < 16; depth++ {
		if _, exists := seen[current.SHA]; exists {
			resolution.Problem = "annotated tag chain contains a cycle"
			return resolution, nil
		}
		seen[current.SHA] = struct{}{}
		tagObject, err := client.TagObject(ctx, repository, current.SHA)
		if err != nil {
			return tagResolution{}, fmt.Errorf("dereference tag %q: %w", tag, err)
		}
		if !commitPattern.MatchString(tagObject.Object.SHA) {
			resolution.Problem = "annotated tag target SHA is not a full lowercase Git object ID"
			return resolution, nil
		}
		switch tagObject.Object.Type {
		case "commit":
			resolution.ResolvedCommit = tagObject.Object.SHA
			return resolution, nil
		case "tag":
			current = tagObject.Object
		default:
			resolution.Problem = fmt.Sprintf(
				"annotated tag ultimately points to unsupported Git object type %q",
				tagObject.Object.Type,
			)
			return resolution, nil
		}
	}
	resolution.Problem = "annotated tag chain exceeds the maximum dereference depth"
	return resolution, nil
}

func (client githubClient) TagObject(ctx context.Context, repository, objectSHA string) (gitTag, error) {
	endpoint := fmt.Sprintf("repos/%s/git/tags/%s", repository, objectSHA)
	output, err := client.runner.Run(ctx, "", "gh", "api", "--method", "GET", endpoint)
	if err != nil {
		return gitTag{}, err
	}
	var tag gitTag
	if err := json.Unmarshal(output, &tag); err != nil {
		return gitTag{}, fmt.Errorf("parse annotated tag object: %w", err)
	}
	return tag, nil
}

func readPullRequest(filename string) (pullRequest, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return pullRequest{}, fmt.Errorf("read pull request metadata: %w", err)
	}
	var pr pullRequest
	if err := json.Unmarshal(contents, &pr); err != nil {
		return pullRequest{}, fmt.Errorf("parse pull request metadata: %w", err)
	}
	return pr, nil
}

func readReleasePlan(filename string) (releasePlan, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return releasePlan{}, fmt.Errorf("read approved release plan: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var plan releasePlan
	if err := decoder.Decode(&plan); err != nil {
		return releasePlan{}, fmt.Errorf("parse approved release plan: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return releasePlan{}, fmt.Errorf("parse approved release plan: %w", err)
	}
	return plan, nil
}

func planDigest(plan releasePlan) (string, error) {
	contents, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode canonical release plan: %w", err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), nil
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

func writeJSON(filename string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %q: %w", filename, err)
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(filename, append(contents, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %q: %w", filename, err)
	}
	return nil
}

func writePlanArtifacts(plan releasePlan, output, summary, githubOutput string) error {
	if err := writeJSON(output, plan); err != nil {
		return err
	}
	if summary != "" {
		if err := appendSummary(summary, plan); err != nil {
			return err
		}
	}
	if githubOutput != "" {
		digest, err := planDigest(plan)
		if err != nil {
			return err
		}
		compact, err := json.Marshal(plan)
		if err != nil {
			return fmt.Errorf("encode plan output: %w", err)
		}
		outputs := map[string]string{
			"merge_sha":   plan.DerivedMergeSHA,
			"plan_digest": digest,
			"plan_json":   string(compact),
			"status":      plan.Status,
		}
		if err := appendGitHubOutputs(githubOutput, outputs); err != nil {
			return err
		}
	}
	return nil
}

func writePublicationArtifacts(
	result publicationResult,
	output string,
	summary string,
	githubOutput string,
) error {
	if err := writeJSON(output, result); err != nil {
		return err
	}
	if summary != "" {
		if err := appendPublicationSummary(summary, result); err != nil {
			return err
		}
	}
	if githubOutput != "" {
		compact, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode publication result output: %w", err)
		}
		outputs := map[string]string{
			"publication_json": string(compact),
			"status":           result.Status,
		}
		if err := appendGitHubOutputs(githubOutput, outputs); err != nil {
			return err
		}
	}
	return nil
}

func appendGitHubOutputs(filename string, outputs map[string]string) error {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open GitHub output file: %w", err)
	}
	defer file.Close()
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := outputs[key]
		if strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("unsafe GitHub output %q", key)
		}
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			return fmt.Errorf("write GitHub output: %w", err)
		}
	}
	return nil
}

func appendSummary(filename string, plan releasePlan) error {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open GitHub Step Summary: %w", err)
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	fmt.Fprintln(writer, "## Generated driver release plan")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "- **Status:** `%s`\n", markdownCell(plan.Status))
	fmt.Fprintf(writer, "- **Source PR:** `#%d`\n", plan.SourcePR)
	fmt.Fprintf(writer, "- **Derived merge SHA:** `%s`\n", markdownCell(plan.DerivedMergeSHA))
	fmt.Fprintf(writer, "- **Upstream source SHA:** `%s`\n", markdownCell(plan.UpstreamSourceSHA))
	fmt.Fprintf(writer, "- **Requested version:** `%s`\n", markdownCell(plan.RequestedVersion))
	fmt.Fprintf(writer, "- **Native interface version:** `%s`\n", markdownCell(plan.NativeInterfaceVersion))
	fmt.Fprintf(writer, "- **Rust driver version:** `%s`\n", markdownCell(plan.RustDriverVersion))
	fmt.Fprintf(writer, "- **Integrity base ref:** `%s`\n", markdownCell(plan.IntegrityValidatorBaseRef))
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "| Module | Proposed tag | Remote state | Kind | Resolved commit |")
	fmt.Fprintln(writer, "|---|---|---|---|---|")
	for _, module := range plan.Modules {
		fmt.Fprintf(
			writer,
			"| `%s` | `%s` | `%s` | `%s` | `%s` |\n",
			markdownCell(module.Path),
			markdownCell(module.Tag),
			markdownCell(module.TagState),
			markdownCell(module.TagKind),
			markdownCell(module.ResolvedCommitSHA),
		)
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "### Reasons")
	for _, reason := range plan.Reasons {
		fmt.Fprintf(writer, "- %s\n", markdownCell(reason))
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "> Phase 1 is read-only. This workflow cannot create, delete, or move tags or publish a GitHub Release.")
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("write GitHub Step Summary: %w", err)
	}
	return nil
}

func appendPublicationSummary(filename string, result publicationResult) error {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open GitHub Step Summary: %w", err)
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	fmt.Fprintln(writer, "## Generated driver publication")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "- **Status:** `%s`\n", markdownCell(result.Status))
	fmt.Fprintf(writer, "- **Source PR:** `#%d`\n", result.SourcePR)
	fmt.Fprintf(writer, "- **Derived merge SHA:** `%s`\n", markdownCell(result.DerivedMergeSHA))
	fmt.Fprintf(writer, "- **Requested version:** `%s`\n", markdownCell(result.RequestedVersion))
	fmt.Fprintf(writer, "- **Approved plan digest:** `%s`\n", markdownCell(result.ApprovedPlanDigest))
	fmt.Fprintf(writer, "- **Final plan digest:** `%s`\n", markdownCell(result.FinalPlanDigest))
	fmt.Fprintf(writer, "- **Atomic push attempted:** `%t`\n", result.PushAttempted)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "| Module | Annotated tag | Remote state | Resolved commit |")
	fmt.Fprintln(writer, "|---|---|---|---|")
	for _, module := range result.Modules {
		fmt.Fprintf(
			writer,
			"| `%s` | `%s` | `%s` | `%s` |\n",
			markdownCell(module.Path),
			markdownCell(module.Tag),
			markdownCell(module.TagState),
			markdownCell(module.ResolvedCommitSHA),
		)
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "### Reasons")
	for _, reason := range result.Reasons {
		fmt.Fprintf(writer, "- %s\n", markdownCell(reason))
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(
		writer,
		"> Publication uses one non-forced atomic push for six annotated tags. It never moves or deletes tags and does not create a GitHub Release.",
	)
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("write GitHub Step Summary: %w", err)
	}
	return nil
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.ReplaceAll(value, "`", "'")
	return value
}
