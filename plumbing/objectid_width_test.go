package plumbing_test

import (
	"bytes"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
)

func TestObjectIDWriteRespectsFormatWidth(t *testing.T) {
	t.Parallel()
	for _, size := range []int{20, 32} {
		var id plumbing.ObjectID
		id.ResetBySize(size)
		data := bytes.Repeat([]byte{0xab}, size+12)
		n, err := id.Write(data)
		if err != nil || n != size {
			t.Fatalf("size %d: Write = %d, %v", size, n, err)
		}
		canonical, ok := plumbing.FromBytes(data[:size])
		if !ok || id != canonical || !id.Equal(canonical) {
			t.Fatalf("size %d: noncanonical object ID", size)
		}
		entries := map[plumbing.ObjectID]bool{id: true}
		if !entries[canonical] {
			t.Fatalf("size %d: object ID cannot be retrieved by its canonical key", size)
		}
	}
}
