package embed

import (
	"strings"
	"testing"

	fastembed "github.com/anush008/fastembed-go"
)

func TestFastEmbedProfilesMatchVendorEnum(t *testing.T) {
	profiles := FastEmbedProfiles()
	if len(profiles) != 6 {
		t.Fatalf("len(FastEmbedProfiles()) = %d, want 6", len(profiles))
	}
	supported := make(map[fastembed.EmbeddingModel]struct{})
	for _, info := range fastembed.ListSupportedModels() {
		supported[info.Model] = struct{}{}
	}
	for _, p := range profiles {
		if p.Enum != fastembed.EmbeddingModel(p.Name) {
			t.Errorf("%s: Enum = %q, want EmbeddingModel(Name)", p.Name, p.Enum)
		}
		if _, ok := supported[p.Enum]; !ok {
			t.Errorf("%s: not in fastembed.ListSupportedModels()", p.Name)
		}
	}
}

func TestLookupEmptyIsDefault(t *testing.T) {
	got, ok := LookupFastEmbedProfile("")
	if !ok {
		t.Fatal("LookupFastEmbedProfile(\"\") = false, want true")
	}
	if got.Name != "fast-bge-small-en-v1.5" {
		t.Fatalf("LookupFastEmbedProfile(\"\") = %q, want fast-bge-small-en-v1.5", got.Name)
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := LookupFastEmbedProfile("nomic-embed-text-v1.5"); ok {
		t.Fatal("LookupFastEmbedProfile(nomic-embed-text-v1.5) = true, want false")
	}
}

func TestIdentitiesIncludePromptScheme(t *testing.T) {
	def, ok := LookupFastEmbedProfile("fast-bge-small-en-v1.5")
	if !ok {
		t.Fatal("default profile missing")
	}
	if def.Identity() != FastEmbedModelID {
		t.Errorf("default identity = %q, want FastEmbedModelID %q", def.Identity(), FastEmbedModelID)
	}
	mini, ok := LookupFastEmbedProfile("fast-all-MiniLM-L6-v2")
	if !ok {
		t.Fatal("MiniLM profile missing")
	}
	if got := mini.Identity(); !strings.Contains(got, "prompts=none") {
		t.Errorf("MiniLM identity %q missing prompts=none", got)
	}
	zh, ok := LookupFastEmbedProfile("fast-bge-small-zh-v1.5")
	if !ok {
		t.Fatal("ZH profile missing")
	}
	if got := zh.Identity(); !strings.Contains(got, "prompts=bge-zh") {
		t.Errorf("ZH identity %q missing prompts=bge-zh", got)
	}
}

func TestKnownFastEmbedModel(t *testing.T) {
	if !KnownFastEmbedModel("") {
		t.Error("KnownFastEmbedModel(\"\") = false, want true")
	}
	for _, p := range FastEmbedProfiles() {
		if !KnownFastEmbedModel(p.Name) {
			t.Errorf("KnownFastEmbedModel(%q) = false, want true", p.Name)
		}
	}
	if KnownFastEmbedModel("unsupported") {
		t.Error("KnownFastEmbedModel(\"unsupported\") = true, want false")
	}
}
