// Command compat executes the Phase 0 compatibility characterization against
// the locked upstream commit and the current tree. It intentionally uses the
// upstream tests as the behavior probes: those tests already assert command
// replies, state, filtering, message order, queue transitions, chunking, and
// representative platform output. The runner compares their canonical test
// outcomes without comparing wall-clock or toolchain noise.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const manifestVersion = 1

type manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Baseline      string    `json:"baseline_commit"`
	Fixtures      []fixture `json:"fixtures"`
}

type fixture struct {
	ID            string   `json:"id"`
	ExistingTests []string `json:"existing_tests"`
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

type result struct {
	FixtureID string `json:"fixture_id"`
	Selector  string `json:"selector"`
	Package   string `json:"package"`
	Test      string `json:"test"`
	Baseline  string `json:"baseline"`
	Current   string `json:"current"`
}

type report struct {
	SchemaVersion int      `json:"schema_version"`
	Baseline      string   `json:"baseline_commit"`
	Current       string   `json:"current_commit"`
	Passed        bool     `json:"passed"`
	Results       []result `json:"results"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "verify" {
		fmt.Fprintln(os.Stderr, "usage: compat verify [--baseline COMMIT] [--report PATH]")
		os.Exit(2)
	}

	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	baselineFlag := flags.String("baseline", "", "locked baseline commit; defaults to compat/manifest.json")
	reportPath := flags.String("report", "", "write the canonical comparison report to this path")
	_ = flags.Parse(os.Args[2:])

	if err := verify(*baselineFlag, *reportPath); err != nil {
		fmt.Fprintf(os.Stderr, "compat verify: %v\n", err)
		os.Exit(1)
	}
}

func verify(baselineOverride, reportPath string) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	manifestData, err := os.ReadFile(filepath.Join(root, "compat", "manifest.json"))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var spec manifest
	if err := json.Unmarshal(manifestData, &spec); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if spec.SchemaVersion != manifestVersion || strings.TrimSpace(spec.Baseline) == "" {
		return errors.New("manifest has no supported baseline commit")
	}
	baseline := spec.Baseline
	if strings.TrimSpace(baselineOverride) != "" {
		if baselineOverride != baseline {
			return fmt.Errorf("requested baseline %s does not match locked manifest baseline %s", baselineOverride, baseline)
		}
		baseline = baselineOverride
	}
	if err := runGit(root, "cat-file", "-e", baseline+"^{commit}"); err != nil {
		return fmt.Errorf("locked baseline %s is not available locally: %w", baseline, err)
	}
	current, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve current commit: %w", err)
	}
	if err := runGit(root, "merge-base", "--is-ancestor", baseline, current); err != nil {
		return fmt.Errorf("locked baseline %s is not an ancestor of current %s: %w", baseline, current, err)
	}

	worktree, err := os.MkdirTemp("", "ddbot-ai-compat-baseline-")
	if err != nil {
		return fmt.Errorf("create baseline worktree: %w", err)
	}
	defer os.RemoveAll(worktree)
	if err := runGit(root, "worktree", "add", "--detach", worktree, baseline); err != nil {
		return fmt.Errorf("create baseline worktree: %w", err)
	}
	defer func() { _ = runGit(root, "worktree", "remove", "--force", worktree) }()

	reportValue := report{SchemaVersion: 1, Baseline: baseline, Current: current, Passed: true}
	for _, fixture := range spec.Fixtures {
		for _, selector := range fixture.ExistingTests {
			pkg, testName, err := parseSelector(selector)
			if err != nil {
				return fmt.Errorf("fixture %s selector %q: %w", fixture.ID, selector, err)
			}
			baseStatus, err := runTest(worktree, pkg, testName)
			if err != nil {
				return fmt.Errorf("baseline %s/%s: %w", pkg, testName, err)
			}
			currentStatus, err := runTest(root, pkg, testName)
			if err != nil {
				return fmt.Errorf("current %s/%s: %w", pkg, testName, err)
			}
			item := result{
				FixtureID: fixture.ID,
				Selector:  selector,
				Package:   pkg,
				Test:      testName,
				Baseline:  baseStatus,
				Current:   currentStatus,
			}
			if baseStatus != currentStatus {
				reportValue.Passed = false
			}
			reportValue.Results = append(reportValue.Results, item)
			fmt.Printf("%-34s baseline=%-6s current=%-6s\n", selector, baseStatus, currentStatus)
		}
	}

	if reportPath != "" {
		data, err := json.MarshalIndent(reportValue, "", "  ")
		if err != nil {
			return fmt.Errorf("encode report: %w", err)
		}
		if err := os.WriteFile(reportPath, append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}
	if !reportValue.Passed {
		return errors.New("semantic test outcome diff detected")
	}
	fmt.Printf("compat verify: PASS (%d probes, baseline=%s, current=%s)\n", len(reportValue.Results), baseline[:12], current[:12])
	return nil
}

func parseSelector(selector string) (string, string, error) {
	separator := strings.LastIndex(selector, "/")
	if separator <= 0 || separator == len(selector)-1 {
		return "", "", errors.New("expected package/TestName")
	}
	pkg := "./" + filepath.ToSlash(selector[:separator])
	testName := selector[separator+1:]
	if !regexp.MustCompile(`^Test[A-Za-z0-9_]+$`).MatchString(testName) {
		return "", "", fmt.Errorf("invalid test name %q", testName)
	}
	return pkg, testName, nil
}

func runTest(dir, pkg, testName string) (string, error) {
	testDir, packageArg := packageLocation(dir, pkg)
	cmd := exec.Command("go", "test", "-json", "-count=1", "-run", "^"+regexp.QuoteMeta(testName)+"$", packageArg)
	cmd.Dir = testDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	status := "missing"
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var event testEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Test != testName {
			continue
		}
		switch event.Action {
		case "pass":
			status = "pass"
		case "fail":
			status = "fail"
		case "skip":
			status = "skip"
		}
	}
	if err != nil && status == "missing" {
		return status, fmt.Errorf("go test failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if status == "missing" {
		return status, errors.New("go test emitted no selected test result")
	}
	return status, nil
}

func packageLocation(root, pkg string) (string, string) {
	// adapter is a nested Go module in the upstream tree. Running `go test
	// ./adapter` from the root module is not equivalent to running the package
	// from its own module, so compatibility selectors retain the short path and
	// this resolver chooses the correct module boundary.
	const adapterPrefix = "./adapter"
	if pkg == adapterPrefix || strings.HasPrefix(pkg, adapterPrefix+string(filepath.Separator)) || strings.HasPrefix(pkg, adapterPrefix+"/") {
		relative := strings.TrimPrefix(pkg, adapterPrefix)
		if relative == "" {
			relative = "."
		} else if !strings.HasPrefix(relative, ".") {
			relative = "." + relative
		}
		return filepath.Join(root, "adapter"), relative
	}
	return root, pkg
}

func repositoryRoot() (string, error) {
	root, err := gitOutput(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	return filepath.Clean(root), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func runGit(dir string, args ...string) error {
	_, err := gitOutput(dir, args...)
	return err
}
