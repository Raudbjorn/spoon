package github

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGemtcSpecimenIdentities(t *testing.T) {
	root := os.Getenv("SPOON_GEMTC_SPECIMEN")
	if root == "" {
		t.Skip("set SPOON_GEMTC_SPECIMEN to the dump that contains gertvv-gemtc, ml-ebs-ext-gemtc, gemtc")
	}
	gertvv := filepath.Join(root, "gertvv-gemtc")
	ml := filepath.Join(root, "ml-ebs-ext-gemtc")
	cran := filepath.Join(root, "gemtc")
	const (
		gertvvHEAD = "b94d86a304eae57c8d16bb4aa8fc3f32155696e4"
		mlHEAD     = "33a5f4d99f4910c8c41c00362196c6cdfcef2f0d"
		sharedBlob = "2ab31ffb668a6a2d10a526c01b0aea18b0de0bc3"
		gertvvBlob = "f48dfb595aa20f96e892310ada66ec68ef750ab8"
	)
	// 1. merge-base(gertvv, ml) == gertvv HEAD
	out, err := exec.Command("git", "-C", ml, "merge-base", gertvvHEAD, mlHEAD).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != gertvvHEAD {
		t.Fatalf("merge-base=%s want %s", got, gertvvHEAD)
	}
	// 2. cran objects are absent from ml
	if err := exec.Command("git", "-C", ml, "cat-file", "-e", "77f55c7e25f3066a0d9fa67354490a3a9fda7e19^{commit}").Run(); err == nil {
		t.Fatal("cran HEAD unexpectedly present in ml")
	}
	// 3. mtc.network.R blob cran==ml != gertvv
	hash := func(repo string) string {
		b, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD:R/mtc.network.R").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	if hash(cran) != sharedBlob || hash(ml) != sharedBlob || hash(gertvv) != gertvvBlob {
		t.Fatalf("blobs cran=%s ml=%s gertvv=%s", hash(cran), hash(ml), hash(gertvv))
	}
	// 4. ml README still names gertvv/gemtc
	readme, err := exec.Command("git", "-C", ml, "show", "HEAD:README.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readme, []byte("gertvv/gemtc")) {
		t.Fatal("ml README no longer names gertvv/gemtc")
	}
}
