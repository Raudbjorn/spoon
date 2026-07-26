package embed

// L2NormalizeAll L2-normalizes each vector in place (zero vectors are left
// untouched), matching OVMS's NormalizeL2(axis=1, eps=1e-12, MAX).
func L2NormalizeAll(vs []Vector) {
	for _, v := range vs {
		l2Normalize(v)
	}
}
