package embed

// Pooling reduction applied to a transformer's last_hidden_state to produce
// one vector per input text. Mirrors OVMS's EmbeddingsCalculatorOV pooling
// modes (embeddings_calculator_ov.proto): CLS, MEAN, LAST.
type Pooling string

const (
	// PoolingCLS takes the hidden state of the first token. OVMS's default;
	// correct for CLS-trained encoders (BGE, arctic-embed, GTE).
	PoolingCLS Pooling = "cls"
	// PoolingMean averages hidden states over the attended positions
	// (attention-mask weighted). Correct for mean-pooled encoders
	// (nomic-embed-text, sentence-transformers MiniLM family).
	PoolingMean Pooling = "mean"
	// PoolingLast takes the hidden state of the last attended token.
	// Used by decoder-style embedders.
	PoolingLast Pooling = "last"
)

// ParsePooling maps a user-supplied string to a Pooling mode. Empty input
// returns PoolingCLS (the OVMS default) and ok=true.
func ParsePooling(s string) (Pooling, bool) {
	switch s {
	case "", "cls", "CLS":
		return PoolingCLS, true
	case "mean", "MEAN":
		return PoolingMean, true
	case "last", "LAST":
		return PoolingLast, true
	default:
		return "", false
	}
}

// PoolHiddenStates reduces a flattened last_hidden_state of shape
// [batch, seq, hidden] to one vector per batch item, following the same
// math as OVMS's post-processing graph (embeddings_servable.cpp):
//
//	CLS:  hidden[b, 0, :]
//	MEAN: sum_t(hidden[b, t, :] * mask[b, t]) / max(sum_t(mask[b, t]), 1e-12)
//	LAST: hidden[b, sum_t(mask[b, t])-1, :]
//
// hidden is row-major [batch*seq*hiddenDim]; mask is row-major [batch*seq]
// with nonzero entries marking attended positions. Vectors are NOT
// normalized here — see L2NormalizeAll.
func PoolHiddenStates(pooling Pooling, hidden []float32, mask []int64, batch, seq, hiddenDim int) []Vector {
	out := make([]Vector, batch)
	for b := 0; b < batch; b++ {
		v := make(Vector, hiddenDim)
		rowBase := b * seq * hiddenDim
		switch pooling {
		case PoolingMean:
			var attended float64
			for t := 0; t < seq; t++ {
				if mask[b*seq+t] == 0 {
					continue
				}
				attended++
				base := rowBase + t*hiddenDim
				for h := 0; h < hiddenDim; h++ {
					v[h] += hidden[base+h]
				}
			}
			if attended < 1 {
				attended = 1 // mirrors OVMS's max(sum_mask, 1e-12) guard
			}
			inv := float32(1 / attended)
			for h := range v {
				v[h] *= inv
			}
		case PoolingLast:
			lastT := 0
			for t := 0; t < seq; t++ {
				if mask[b*seq+t] != 0 {
					lastT = t
				}
			}
			copy(v, hidden[rowBase+lastT*hiddenDim:rowBase+(lastT+1)*hiddenDim])
		default: // PoolingCLS
			copy(v, hidden[rowBase:rowBase+hiddenDim])
		}
		out[b] = v
	}
	return out
}

// L2NormalizeAll L2-normalizes each vector in place (zero vectors are left
// untouched), matching OVMS's NormalizeL2(axis=1, eps=1e-12, MAX).
func L2NormalizeAll(vs []Vector) {
	for _, v := range vs {
		l2Normalize(v)
	}
}
