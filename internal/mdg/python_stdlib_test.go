package mdg

import "testing"

func TestIsPythonStdlib_KnownNames(t *testing.T) {
	// A handful of well-known stdlib modules. The test is intentionally
	// not exhaustive — we just want a smoke-level check that the list is
	// reasonable and the lookup function works.
	for _, name := range []string{"os", "sys", "json", "io", "typing", "asyncio", "pathlib"} {
		if !isPythonStdlib(name) {
			t.Errorf("%q should be classified as stdlib", name)
		}
	}
}

func TestIsPythonStdlib_NotStdlib(t *testing.T) {
	for _, name := range []string{"numpy", "requests", "pkg.sub.mod", "", "django"} {
		if isPythonStdlib(name) {
			t.Errorf("%q should NOT be classified as stdlib", name)
		}
	}
}

func TestIsPythonStdlib_NamespaceSegment(t *testing.T) {
	// "os.path" should classify as stdlib by virtue of its first segment.
	if !isPythonStdlib("os.path") {
		t.Errorf("os.path should be stdlib")
	}
	// "json.tool" — same.
	if !isPythonStdlib("json.tool") {
		t.Errorf("json.tool should be stdlib")
	}
}
