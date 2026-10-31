// A fixture for scripts/test_tripwire_hash.py, not code anything builds: its bytes are the golden
// input that pins the body_sha256 definition (TP-134). Edit it and that test fails, which is the
// point — the definition of what is hashed should be hard to change without noticing. The Go
// toolchain ignores a testdata directory, and this one is outside the api module either way.
package testdata

import "testing"

// This comment is above the function and so is NOT part of the slice, which starts at `func`.
func TestTheSliceStartsAtFunc(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("arithmetic has moved")
	}
	// An indented closing brace does not end the slice; only one in column 1 does.
	for i := 0; i < 2; i++ {
		t.Log(i)
	}
}

func TestTheNextFunctionIsNotInTheSlice(t *testing.T) {
	t.Log("second")
}
