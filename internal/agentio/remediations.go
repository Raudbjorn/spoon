// internal/agentio/remediations.go
package agentio

import "fmt"

// RemediationBadInput names the subcommand the user should consult.
// noun and verb may be empty for top-level usage errors.
func RemediationBadInput(noun, verb string) string {
	cmd := "spn"
	if noun != "" {
		cmd += " " + noun
		if verb != "" {
			cmd += " " + verb
		}
	}
	return fmt.Sprintf("Run `%s --help` to see accepted forms. PR refs accept `owner/repo#42`, a full URL, or `#42` from inside a git checkout.", cmd)
}

func RemediationAuthRequired() string {
	return "Authenticate with `spoon auth login` (GitHub) or set `GITLAB_TOKEN` (GitLab), then retry."
}

func RemediationAuthScope(scope string) string {
	return fmt.Sprintf("Authorize with the required scope: `spoon auth login --scope %s`, then retry.", scope)
}

func RemediationPolicyBodyRequired(prRef, threadID string) string {
	return fmt.Sprintf("Resolve with an explanation: `spn threads resolve %s %s --body \"<what you fixed, or why no change was needed>\"`.", prRef, threadID)
}

func RemediationPolicyBulkHumanThreads(prRef string) string {
	return fmt.Sprintf("Some threads need individual responses. List them with `spn threads list %s`, then resolve each with `spn threads resolve %s <id> --body \"...\"`."+
		" Bulk-resolve will not touch human-raised threads.", prRef, prRef)
}

func RemediationNotFound() string {
	return "Verify the PR / repo / thread exists and that your token has access. PR refs and IDs are case-sensitive."
}

func RemediationUpstream() string {
	return "Provider API failed. Retry in a few seconds. If persistent, check the provider's status page."
}

func RemediationRateLimited(resetAt string, retryAfterSec int) string {
	return fmt.Sprintf("Rate limit exceeded. Wait until %s (%ds), then retry. Authenticate (`spoon auth login`) for a higher limit.", resetAt, retryAfterSec)
}

func RemediationResolvePartialFailure(prRef, threadID string) string {
	return fmt.Sprintf("Retry: `spn threads resolve %s %s` (omit --body; the comment is already posted).", prRef, threadID)
}

func RemediationInternal() string {
	return "Unexpected error. Re-run with the same arguments; if it persists, report at the project's issue tracker with the full stderr output."
}
