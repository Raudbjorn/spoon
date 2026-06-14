package embed

// In-process OpenVINO cross-encoder reranker. Reimplements OVMS's /v3/rerank
// computation (RerankCalculatorOV) without the server: query and documents
// are tokenized by the converted tokenizer model, framed with the pair
// template parsed from tokenizer.json (pre ++ query ++ mid ++ doc ++ post —
// OVMS's "BOS query EOS SEP doc EOS" schema), scored by the cross-encoder,
// and squashed with a sigmoid. Long documents are truncated to fit the
// model context (OVMS chunks + max-aggregates instead; spoon's fork digests
// are bounded, so truncation loses nothing in practice).
//
// Shares the dlopen-loaded OpenVINO C runtime with openvino.go (ovffi.c /
// ovload.go); no build tag, no build-time OpenVINO SDK.

/*
#include <stdlib.h>
#include "ovffi.h"
*/
import "C"

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"unsafe"
)

// Reranker scores (query, document) relevance with a cross-encoder run via
// the OpenVINO C API. Create with NewReranker; call Close when done. Safe
// for concurrent use (calls are serialized internally).
type Reranker struct {
	cfg  RerankConfig
	tmpl pairTemplate

	mu       sync.Mutex
	core     *C.ov_core_t
	tokModel *C.ov_compiled_model_t
	tokReq   *C.ov_infer_request_t
	rrModel  *C.ov_compiled_model_t
	rrReq    *C.ov_infer_request_t
	rrInputs map[string]bool
	closed   bool
}

// NewReranker loads and compiles the tokenizer (CPU) and cross-encoder
// (cfg.Device) from cfg.ModelPath.
func NewReranker(cfg RerankConfig) (*Reranker, error) {
	if !ovEnsureLoaded() {
		return nil, errOpenVINOUnavailable()
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	tmpl, err := loadPairTemplate(filepath.Join(cfg.ModelPath, "tokenizer.json"))
	if err != nil {
		return nil, err
	}
	r := &Reranker{cfg: cfg, tmpl: tmpl}

	if status := C.ov_core_create(&r.core); status != C.OK {
		return nil, ovErr("create core", status)
	}
	cleanupOnErr := func(err error) (*Reranker, error) {
		r.Close()
		return nil, err
	}

	cLib := C.CString(cfg.TokenizersLib)
	status := C.ov_core_add_extension(r.core, cLib)
	C.free(unsafe.Pointer(cLib))
	if status != C.OK {
		return cleanupOnErr(ovErr("load tokenizers extension "+cfg.TokenizersLib, status))
	}

	var tokErr error
	r.tokModel, r.tokReq, tokErr = ovCompileXML(r.core,
		filepath.Join(cfg.ModelPath, "openvino_tokenizer.xml"), "CPU", "")
	if tokErr != nil {
		return cleanupOnErr(tokErr)
	}
	var rrErr error
	r.rrModel, r.rrReq, rrErr = ovCompileXML(r.core,
		filepath.Join(cfg.ModelPath, "openvino_model.xml"), cfg.Device, cfg.CacheDir)
	if rrErr != nil {
		return cleanupOnErr(rrErr)
	}
	names, err := compiledInputNames(r.rrModel)
	if err != nil {
		return cleanupOnErr(err)
	}
	r.rrInputs = names
	return r, nil
}

// Close releases all OpenVINO resources.
func (r *Reranker) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	if r.tokReq != nil {
		C.ov_infer_request_free(r.tokReq)
	}
	if r.rrReq != nil {
		C.ov_infer_request_free(r.rrReq)
	}
	if r.tokModel != nil {
		C.ov_compiled_model_free(r.tokModel)
	}
	if r.rrModel != nil {
		C.ov_compiled_model_free(r.rrModel)
	}
	if r.core != nil {
		C.ov_core_free(r.core)
	}
}

// Rerank returns one relevance score in [0,1] per document (sigmoid of the
// cross-encoder logit, exactly as OVMS computes it).
func (r *Reranker) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("openvino reranker: closed")
	}
	if len(docs) == 0 {
		return nil, nil
	}

	rows, err := r.buildPairRows(query, docs)
	if err != nil {
		return nil, err
	}

	scores := make([]float64, 0, len(docs))
	for start := 0; start < len(rows); start += r.cfg.MaxBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := start + r.cfg.MaxBatch
		if end > len(rows) {
			end = len(rows)
		}
		batch, err := r.scoreBatch(rows[start:end])
		if err != nil {
			return nil, err
		}
		scores = append(scores, batch...)
	}
	return scores, nil
}

// buildPairRows tokenizes query + docs and assembles framed pair rows.
func (r *Reranker) buildPairRows(query string, docs []string) ([][]int64, error) {
	texts := make([]string, 0, len(docs)+1)
	texts = append(texts, nonEmpty(query))
	for _, d := range docs {
		texts = append(texts, nonEmpty(d))
	}
	ids, mask, seq, err := ovTokenize(r.tokReq, texts)
	if err != nil {
		return nil, err
	}

	content := make([][]int64, len(texts))
	for i := range texts {
		attended := 0
		for t := 0; t < seq; t++ {
			if mask[i*seq+t] != 0 {
				attended++
			}
		}
		row := ids[i*seq : i*seq+attended]
		content[i] = r.tmpl.stripSingle(row)
	}

	q := content[0]
	rows := make([][]int64, len(docs))
	for i := range docs {
		row := r.tmpl.assemble(q, content[i+1], r.cfg.MaxTokens)
		if row == nil {
			return nil, fmt.Errorf("openvino reranker: query (%d tokens) exceeds the model context (%d); shorten the query", len(q), r.cfg.MaxTokens)
		}
		rows[i] = row
	}
	return rows, nil
}

// scoreBatch pads rows to a rectangle, runs the cross-encoder, and returns
// sigmoid(logit) per row.
func (r *Reranker) scoreBatch(rows [][]int64) ([]float64, error) {
	batch := len(rows)
	seq := 0
	for _, row := range rows {
		if len(row) > seq {
			seq = len(row)
		}
	}
	ids := make([]int64, batch*seq)
	mask := make([]int64, batch*seq)
	for i, row := range rows {
		copy(ids[i*seq:], row)
		for t := range row {
			mask[i*seq+t] = 1
		}
	}

	var shape C.ov_shape_t
	dims := []C.int64_t{C.int64_t(batch), C.int64_t(seq)}
	if status := C.ov_shape_create(2, &dims[0], &shape); status != C.OK {
		return nil, ovErr("create reranker input shape", status)
	}
	defer C.ov_shape_free(&shape)

	var keep []*C.ov_tensor_t
	defer func() {
		for _, t := range keep {
			C.ov_tensor_free(t)
		}
	}()
	set := func(name string, data []int64) error {
		var t *C.ov_tensor_t
		if status := C.ov_tensor_create(C.I64, shape, &t); status != C.OK {
			return ovErr("create "+name+" tensor", status)
		}
		keep = append(keep, t)
		var raw unsafe.Pointer
		if status := C.ov_tensor_data(t, &raw); status != C.OK {
			return ovErr("map "+name+" tensor", status)
		}
		copy(unsafe.Slice((*int64)(raw), len(data)), data)
		cName := C.CString(name)
		status := C.ov_infer_request_set_tensor(r.rrReq, cName, t)
		C.free(unsafe.Pointer(cName))
		if status != C.OK {
			return ovErr("set "+name, status)
		}
		return nil
	}
	if err := set("input_ids", ids); err != nil {
		return nil, err
	}
	if err := set("attention_mask", mask); err != nil {
		return nil, err
	}
	if r.rrInputs["token_type_ids"] {
		// OVMS zero-fills token_type_ids for 3-input cross-encoders.
		if err := set("token_type_ids", make([]int64, batch*seq)); err != nil {
			return nil, err
		}
	}

	if status := C.ov_infer_request_infer(r.rrReq); status != C.OK {
		return nil, ovErr("reranker inference", status)
	}

	logits, lShape, err := f32OutputTensor(r.rrReq, "logits")
	if err != nil {
		return nil, err
	}
	if len(lShape) != 2 || int(lShape[0]) != batch {
		return nil, fmt.Errorf("openvino reranker: logits shape %v for batch %d", lShape, batch)
	}
	k := int(lShape[1])
	out := make([]float64, batch)
	for i := 0; i < batch; i++ {
		// OVMS: with >1 logit take index 1 (positive class), else index 0;
		// then sigmoid.
		logit := logits[i*k]
		if k > 1 {
			logit = logits[i*k+1]
		}
		out[i] = 1 / (1 + math.Exp(-float64(logit)))
	}
	return out, nil
}

// f32OutputTensor fetches a named output tensor (falling back to output 0
// when the name is absent) and returns its float32 data and shape.
func f32OutputTensor(req *C.ov_infer_request_t, name string) ([]float32, []int64, error) {
	var t *C.ov_tensor_t
	cName := C.CString(name)
	status := C.ov_infer_request_get_tensor(req, cName, &t)
	C.free(unsafe.Pointer(cName))
	if status != C.OK || t == nil {
		if status := C.ov_infer_request_get_output_tensor_by_index(req, 0, &t); status != C.OK {
			return nil, nil, ovErr("get output tensor", status)
		}
	}
	defer C.ov_tensor_free(t)

	shape, err := tensorShape(t)
	if err != nil {
		return nil, nil, err
	}
	var et C.ov_element_type_e
	if status := C.ov_tensor_get_element_type(t, &et); status != C.OK {
		return nil, nil, ovErr("get output element type", status)
	}
	if et != C.F32 {
		return nil, nil, fmt.Errorf("openvino: output element type %d, want f32", int(et))
	}
	n := 1
	for _, d := range shape {
		n *= int(d)
	}
	var raw unsafe.Pointer
	if status := C.ov_tensor_data(t, &raw); status != C.OK {
		return nil, nil, ovErr("map output tensor", status)
	}
	out := make([]float32, n)
	copy(out, unsafe.Slice((*float32)(raw), n))
	return out, shape, nil
}

func nonEmpty(s string) string {
	if s == "" {
		return " "
	}
	return s
}
