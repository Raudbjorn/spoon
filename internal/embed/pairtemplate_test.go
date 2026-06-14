package embed

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTokenizerJSON(t *testing.T, postProcessor string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tokenizer.json")
	content := `{"version":"1.0","post_processor":` + postProcessor + `}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// xlmrPostProcessor mirrors the real bge-reranker-base tokenizer.json:
// single = <s> A </s>, pair = <s> A </s> </s> B </s>.
const xlmrPostProcessor = `{
 "type": "TemplateProcessing",
 "single": [
  {"SpecialToken": {"id": "<s>", "type_id": 0}},
  {"Sequence": {"id": "A", "type_id": 0}},
  {"SpecialToken": {"id": "</s>", "type_id": 0}}
 ],
 "pair": [
  {"SpecialToken": {"id": "<s>", "type_id": 0}},
  {"Sequence": {"id": "A", "type_id": 0}},
  {"SpecialToken": {"id": "</s>", "type_id": 0}},
  {"SpecialToken": {"id": "</s>", "type_id": 0}},
  {"Sequence": {"id": "B", "type_id": 0}},
  {"SpecialToken": {"id": "</s>", "type_id": 0}}
 ],
 "special_tokens": {
  "</s>": {"id": "</s>", "ids": [2], "tokens": ["</s>"]},
  "<s>": {"id": "<s>", "ids": [0], "tokens": ["<s>"]}
 }
}`

func TestLoadPairTemplate_TemplateProcessing(t *testing.T) {
	pt, err := loadPairTemplate(writeTokenizerJSON(t, xlmrPostProcessor))
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.pre) != 1 || pt.pre[0] != 0 {
		t.Errorf("pre = %v, want [0]", pt.pre)
	}
	if len(pt.mid) != 2 || pt.mid[0] != 2 || pt.mid[1] != 2 {
		t.Errorf("mid = %v, want [2 2]", pt.mid)
	}
	if len(pt.post) != 1 || pt.post[0] != 2 {
		t.Errorf("post = %v, want [2]", pt.post)
	}
	if pt.singleLead != 1 || pt.singleTrail != 1 {
		t.Errorf("single lead/trail = %d/%d, want 1/1", pt.singleLead, pt.singleTrail)
	}
}

func TestLoadPairTemplate_Roberta(t *testing.T) {
	pt, err := loadPairTemplate(writeTokenizerJSON(t,
		`{"type":"RobertaProcessing","sep":["</s>",2],"cls":["<s>",0],"trim_offsets":true,"add_prefix_space":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.pre) != 1 || pt.pre[0] != 0 || len(pt.mid) != 2 || len(pt.post) != 1 {
		t.Errorf("roberta template wrong: pre=%v mid=%v post=%v", pt.pre, pt.mid, pt.post)
	}
}

func TestLoadPairTemplate_Bert(t *testing.T) {
	pt, err := loadPairTemplate(writeTokenizerJSON(t,
		`{"type":"BertProcessing","sep":["[SEP]",102],"cls":["[CLS]",101]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.pre) != 1 || pt.pre[0] != 101 || len(pt.mid) != 1 || pt.mid[0] != 102 {
		t.Errorf("bert template wrong: pre=%v mid=%v post=%v", pt.pre, pt.mid, pt.post)
	}
}

func TestLoadPairTemplate_MissingFileFallsBack(t *testing.T) {
	pt, err := loadPairTemplate(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.pre)+len(pt.mid)+len(pt.post) != 0 || pt.singleLead != 0 || pt.singleTrail != 0 {
		t.Errorf("missing file should give bare template, got %+v", pt)
	}
}

func TestPairTemplate_StripAndAssemble(t *testing.T) {
	pt := pairTemplate{pre: []int64{0}, mid: []int64{2, 2}, post: []int64{2}, singleLead: 1, singleTrail: 1}

	// stripSingle: <s> 7 8 </s> → 7 8
	got := pt.stripSingle([]int64{0, 7, 8, 2})
	if len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Errorf("stripSingle = %v, want [7 8]", got)
	}
	if pt.stripSingle([]int64{0}) != nil {
		t.Error("stripSingle on too-short row should return nil")
	}

	// assemble: <s> q </s></s> d </s>
	row := pt.assemble([]int64{7}, []int64{8, 9}, 0)
	want := []int64{0, 7, 2, 2, 8, 9, 2}
	if len(row) != len(want) {
		t.Fatalf("assemble = %v, want %v", row, want)
	}
	for i := range want {
		if row[i] != want[i] {
			t.Fatalf("assemble = %v, want %v", row, want)
		}
	}

	// Truncation: maxTokens 6 → doc shrinks to 1 token.
	row = pt.assemble([]int64{7}, []int64{8, 9}, 6)
	if len(row) != 6 || row[4] != 8 {
		t.Errorf("truncated assemble = %v, want doc cut to [8]", row)
	}
	// Query alone exceeding budget → nil.
	if pt.assemble([]int64{7, 7, 7}, []int64{8}, 6) != nil {
		t.Error("assemble should return nil when query+specials exceed budget")
	}
}
