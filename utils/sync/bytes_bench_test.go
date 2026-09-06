package sync_test

import (
	"testing"

	gogitsync "github.com/go-git/go-git/v6/utils/sync"
)

func BenchmarkByteSlicePool(b *testing.B) {
	gogitsync.PutByteSlice(gogitsync.GetByteSlice())
	b.ReportAllocs()
	for b.Loop() {
		buf := gogitsync.GetByteSlice()
		gogitsync.PutByteSlice(buf)
	}
}

func TestByteSlicePoolClearsContents(t *testing.T) {
	t.Parallel()
	buf := gogitsync.GetByteSlice()
	for i := range *buf {
		(*buf)[i] = 0xff
	}
	*buf = (*buf)[:1]
	gogitsync.PutByteSlice(buf)
	buf = gogitsync.GetByteSlice()
	defer gogitsync.PutByteSlice(buf)
	if len(*buf) < 32*1024 {
		t.Fatalf("buffer length = %d", len(*buf))
	}
	for i, b := range *buf {
		if b != 0 {
			t.Fatalf("byte %d was not cleared", i)
		}
	}
}
