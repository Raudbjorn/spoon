package embed

import (
	"encoding/json"
	"fmt"
	"os"
)

// pairTemplate describes how a cross-encoder's tokenizer frames a
// (sequence A, sequence B) pair with special tokens, parsed from the
// HuggingFace tokenizer.json post_processor. It also records how many
// special tokens the SINGLE-sequence template places around the content,
// so content tokens can be recovered from the output of the converted
// openvino_tokenizer.xml (which always applies the single template).
//
//	pair row = pre ++ A ++ mid ++ B ++ post
type pairTemplate struct {
	pre  []int64 // ids before sequence A (e.g. [CLS] / <s>)
	mid  []int64 // ids between A and B   (e.g. [SEP] / </s></s>)
	post []int64 // ids after sequence B  (e.g. [SEP] / </s>)

	singleLead  int // specials before A in the single template
	singleTrail int // specials after A in the single template
}

// tokenizerJSON is the subset of tokenizer.json we parse.
type tokenizerJSON struct {
	PostProcessor json.RawMessage `json:"post_processor"`
}

type postProcessorHeader struct {
	Type string `json:"type"`
}

// templateProcessing models the "TemplateProcessing" post-processor.
type templateProcessing struct {
	Single        []templatePiece                  `json:"single"`
	Pair          []templatePiece                  `json:"pair"`
	SpecialTokens map[string]templateSpecialTokens `json:"special_tokens"`
}

type templatePiece struct {
	SpecialToken *struct {
		ID string `json:"id"`
	} `json:"SpecialToken"`
	Sequence *struct {
		ID string `json:"id"` // "A" or "B"
	} `json:"Sequence"`
}

type templateSpecialTokens struct {
	IDs []int64 `json:"ids"`
}

// tokenPair is the ["token", id] tuple used by Roberta/Bert post-processors.
type tokenPair struct {
	id int64
}

func (t *tokenPair) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 2 {
		return fmt.Errorf("expected [token, id] pair, got %d elements", len(raw))
	}
	return json.Unmarshal(raw[1], &t.id)
}

type robertaProcessing struct {
	Sep tokenPair `json:"sep"`
	Cls tokenPair `json:"cls"`
}

// loadPairTemplate parses <modelDir>/tokenizer.json and derives the pair
// template. Falls back to a bare template (no specials, sequences plainly
// concatenated) when the file or post_processor is absent — mirroring
// OVMS's no-BOS path, which concatenates query+document with no framing.
func loadPairTemplate(tokenizerJSONPath string) (pairTemplate, error) {
	data, err := os.ReadFile(tokenizerJSONPath)
	if err != nil {
		return pairTemplate{}, nil // no tokenizer.json → bare concat
	}
	var tj tokenizerJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return pairTemplate{}, fmt.Errorf("parse %s: %w", tokenizerJSONPath, err)
	}
	if len(tj.PostProcessor) == 0 || string(tj.PostProcessor) == "null" {
		return pairTemplate{}, nil
	}
	var head postProcessorHeader
	if err := json.Unmarshal(tj.PostProcessor, &head); err != nil {
		return pairTemplate{}, fmt.Errorf("parse post_processor: %w", err)
	}

	switch head.Type {
	case "TemplateProcessing":
		var tp templateProcessing
		if err := json.Unmarshal(tj.PostProcessor, &tp); err != nil {
			return pairTemplate{}, fmt.Errorf("parse TemplateProcessing: %w", err)
		}
		return templateToPair(tp)
	case "RobertaProcessing":
		var rp robertaProcessing
		if err := json.Unmarshal(tj.PostProcessor, &rp); err != nil {
			return pairTemplate{}, fmt.Errorf("parse RobertaProcessing: %w", err)
		}
		// <s> A </s> </s> B </s>
		return pairTemplate{
			pre:         []int64{rp.Cls.id},
			mid:         []int64{rp.Sep.id, rp.Sep.id},
			post:        []int64{rp.Sep.id},
			singleLead:  1,
			singleTrail: 1,
		}, nil
	case "BertProcessing":
		var bp robertaProcessing // same shape: sep + cls
		if err := json.Unmarshal(tj.PostProcessor, &bp); err != nil {
			return pairTemplate{}, fmt.Errorf("parse BertProcessing: %w", err)
		}
		// [CLS] A [SEP] B [SEP]
		return pairTemplate{
			pre:         []int64{bp.Cls.id},
			mid:         []int64{bp.Sep.id},
			post:        []int64{bp.Sep.id},
			singleLead:  1,
			singleTrail: 1,
		}, nil
	default:
		// Unknown post-processor (e.g. a Sequence of processors): bare
		// concat keeps reranking functional, if slightly off-template.
		return pairTemplate{}, nil
	}
}

// templateToPair converts a TemplateProcessing spec into a pairTemplate.
func templateToPair(tp templateProcessing) (pairTemplate, error) {
	idOf := func(name string) ([]int64, error) {
		st, ok := tp.SpecialTokens[name]
		if !ok || len(st.IDs) == 0 {
			return nil, fmt.Errorf("special token %q has no ids in tokenizer.json", name)
		}
		return st.IDs, nil
	}

	var out pairTemplate
	// Single template: count specials around A.
	seenA := false
	for _, p := range tp.Single {
		switch {
		case p.Sequence != nil && p.Sequence.ID == "A":
			seenA = true
		case p.SpecialToken != nil && !seenA:
			out.singleLead++
		case p.SpecialToken != nil && seenA:
			out.singleTrail++
		}
	}

	// Pair template: pre (before A), mid (between A and B), post (after B).
	stage := 0 // 0 = pre, 1 = mid, 2 = post
	for _, p := range tp.Pair {
		switch {
		case p.Sequence != nil && p.Sequence.ID == "A":
			stage = 1
		case p.Sequence != nil && p.Sequence.ID == "B":
			stage = 2
		case p.SpecialToken != nil:
			ids, err := idOf(p.SpecialToken.ID)
			if err != nil {
				return pairTemplate{}, err
			}
			switch stage {
			case 0:
				out.pre = append(out.pre, ids...)
			case 1:
				out.mid = append(out.mid, ids...)
			case 2:
				out.post = append(out.post, ids...)
			}
		}
	}
	return out, nil
}

// stripSingle removes the single-template special tokens from a content row
// (already trimmed to its attended length).
func (t pairTemplate) stripSingle(row []int64) []int64 {
	if len(row) < t.singleLead+t.singleTrail {
		return nil
	}
	return row[t.singleLead : len(row)-t.singleTrail]
}

// assemble builds one pair row: pre ++ a ++ mid ++ b ++ post, truncating b
// so the row fits maxTokens (a is never truncated; returns nil when even an
// empty b cannot fit).
func (t pairTemplate) assemble(a, b []int64, maxTokens int) []int64 {
	overhead := len(t.pre) + len(t.mid) + len(t.post) + len(a)
	if maxTokens > 0 && overhead >= maxTokens {
		return nil
	}
	if maxTokens > 0 && overhead+len(b) > maxTokens {
		b = b[:maxTokens-overhead]
	}
	row := make([]int64, 0, overhead+len(b))
	row = append(row, t.pre...)
	row = append(row, a...)
	row = append(row, t.mid...)
	row = append(row, b...)
	row = append(row, t.post...)
	return row
}
