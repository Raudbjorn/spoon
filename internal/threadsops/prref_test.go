package threadsops

import "testing"

func TestParsePRRef_ownerRepoHash(t *testing.T) {
	owner, repo, n, err := ParsePRRef("owner/repo#42", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if owner != "owner" || repo != "repo" || n != 42 {
		t.Errorf("got (%q,%q,%d)", owner, repo, n)
	}
}

func TestParsePRRef_url(t *testing.T) {
	owner, repo, n, err := ParsePRRef("https://github.com/owner/repo/pull/123", "", "")
	if err != nil || owner != "owner" || repo != "repo" || n != 123 {
		t.Errorf("got (%q,%q,%d) err=%v", owner, repo, n, err)
	}
}

func TestParsePRRef_hashOnly_withFallback(t *testing.T) {
	owner, repo, n, err := ParsePRRef("#5", "fb", "fr")
	if err != nil || owner != "fb" || repo != "fr" || n != 5 {
		t.Errorf("got (%q,%q,%d) err=%v", owner, repo, n, err)
	}
}

func TestParsePRRef_hashOnly_noFallback(t *testing.T) {
	_, _, _, err := ParsePRRef("#5", "", "")
	if err == nil {
		t.Error("expected error when no fallback")
	}
}

func TestParsePRRef_gitlabURLRejected(t *testing.T) {
	_, _, _, err := ParsePRRef("https://gitlab.com/g/r/merge_requests/1", "", "")
	if err == nil {
		t.Error("expected error for non-github URL")
	}
}
