// Package workflow finds jobs that LocalCI routes to a developer's Mac but that
// use secrets, an environment or OIDC. Those belong on hosted runners.
package workflow

import (
	"regexp"
	"slices"
	"strings"
)

// RoutingMarker is how a job opts in to LocalCI: its runs-on reads this variable.
const RoutingMarker = "LOCALCI_RUNNERS"

// Finding is a routed job and what it uses that it shouldn't.
type Finding struct {
	Job  string
	What string // "secrets.NAME", "secrets", "environment" or "id-token: write"
}

var (
	secretRef = regexp.MustCompile(`\bsecrets(?:\.([A-Za-z_][A-Za-z0-9_-]*)|\s*\[|\s*\))`)
	idToken   = regexp.MustCompile(`\bid-token\s*:\s*write\b`)
	envKey    = regexp.MustCompile(`^environment\s*:`)
)

// Check scans one workflow file. Workflow-level lines (env, permissions) count
// for every job.
//
// ponytail: a line scan, not a YAML parser. Block-style YAML only; flow-style
// mappings or anchors can hide a job or a secret. Parse YAML if that bites.
func Check(src []byte) []Finding {
	var shared []string
	type job struct {
		id        string
		lines     []string
		keyIndent int  // indent of the job's own keys (runs-on, environment, ...)
		env       bool // has a job-level environment: key, not an action input of that name
	}
	var jobs []*job
	inJobs, jobIndent := false, -1
	for line := range strings.Lines(string(src)) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case indent == 0:
			inJobs = strings.HasPrefix(trimmed, "jobs:")
			if !inJobs {
				shared = append(shared, line)
			}
		case !inJobs:
			shared = append(shared, line)
		case jobIndent < 0 || indent == jobIndent:
			jobIndent = indent
			id, _, _ := strings.Cut(trimmed, ":")
			jobs = append(jobs, &job{id: strings.Trim(id, `"'`), keyIndent: -1})
		case indent > jobIndent && len(jobs) > 0:
			j := jobs[len(jobs)-1]
			if j.keyIndent < 0 {
				j.keyIndent = indent
			}
			j.env = j.env || indent == j.keyIndent && envKey.MatchString(trimmed)
			j.lines = append(j.lines, line)
		}
	}

	var out []Finding
	for _, j := range jobs {
		if !strings.Contains(strings.Join(j.lines, "\n"), RoutingMarker) {
			continue
		}
		seen := map[string]bool{}
		if j.env {
			out = append(out, Finding{Job: j.id, What: "environment"})
		}
		for _, line := range slices.Concat(shared, j.lines) {
			for _, what := range uses(line) {
				if !seen[what] {
					seen[what] = true
					out = append(out, Finding{Job: j.id, What: what})
				}
			}
		}
	}
	return out
}

// uses lists what one line references. GITHUB_TOKEN is fine: every job gets it.
func uses(line string) []string {
	var what []string
	for _, m := range secretRef.FindAllStringSubmatch(line, -1) {
		switch m[1] {
		case "GITHUB_TOKEN":
		case "":
			what = append(what, "secrets")
		default:
			what = append(what, "secrets."+m[1])
		}
	}
	if idToken.MatchString(line) {
		what = append(what, "id-token: write")
	}
	return what
}
