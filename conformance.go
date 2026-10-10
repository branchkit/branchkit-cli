package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// GitHub names an Actions check run after its JOB, never after the workflow.
// The scaffold templates give the conformance job this name. Workflows
// scaffolded before they did leave the job unnamed, so its check run carries
// the job id instead; both are accepted, the named run first.
const (
	conformanceCheckName       = "BranchKit Conformance"
	legacyConformanceCheckName = "conformance"
)

type conformanceStatus struct {
	Status string // "passed", "failed", "pending", "unknown" — derived from
	// GitHub's raw conclusion, which used to be kept beside it and read by
	// nothing.
	Tag string
}

type ghCheckRunsResponse struct {
	CheckRuns []ghCheckRun `json:"check_runs"`
}

type ghCheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func fetchConformanceStatus(source ResolvedSource, tag string) conformanceStatus {
	if tag == "" {
		return conformanceStatus{Status: "unknown", Tag: tag}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s/check-runs", source.Owner, source.Repo, tag)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return conformanceStatus{Status: "unknown", Tag: tag}
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return conformanceStatus{Status: "unknown", Tag: tag}
	}
	defer resp.Body.Close()

	var result ghCheckRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return conformanceStatus{Status: "unknown", Tag: tag}
	}
	return conformanceFromCheckRuns(result.CheckRuns, tag)
}

// conformanceFromCheckRuns reads the conformance result out of a commit's
// check runs.
func conformanceFromCheckRuns(runs []ghCheckRun, tag string) conformanceStatus {
	for _, name := range []string{conformanceCheckName, legacyConformanceCheckName} {
		for _, run := range runs {
			if run.Name != name {
				continue
			}
			switch {
			case run.Status != "completed":
				return conformanceStatus{Status: "pending", Tag: tag}
			case run.Conclusion == "success":
				return conformanceStatus{Status: "passed", Tag: tag}
			default:
				return conformanceStatus{Status: "failed", Tag: tag}
			}
		}
	}
	return conformanceStatus{Status: "unknown", Tag: tag}
}

func formatConformanceStatus(cs conformanceStatus) string {
	switch cs.Status {
	case "passed":
		return fmt.Sprintf("  Conformance: passed (%s)", cs.Tag)
	case "failed":
		return fmt.Sprintf("  Conformance: FAILED (%s)", cs.Tag)
	case "pending":
		return fmt.Sprintf("  Conformance: in progress (%s)", cs.Tag)
	default:
		return "  Conformance: unknown (no CI badge found)"
	}
}
