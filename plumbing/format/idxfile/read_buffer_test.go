package idxfile

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndexReadBuffer(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("index entries"), 10000)
	r := &indexReadBuffer{source: bytes.NewReader(data)}
	for _, off := range []int64{0, 1023, 32760, 32768, 65535, 42, int64(len(data) - 5), int64(len(data)), -1} {
		for _, size := range []int{4, 20, 32, 65536} {
			got, want := make([]byte, size), make([]byte, size)
			n, err := r.ReadAt(got, off)
			wantN, wantErr := bytes.NewReader(data).ReadAt(want, off)
			require.Equal(t, wantN, n)
			require.Equal(t, want, got)
			if wantErr == nil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, errors.Is(wantErr, io.EOF), errors.Is(err, io.EOF))
			}
		}
	}
}
