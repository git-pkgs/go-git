package idxfile

import "io"

// indexReadBuffer coalesces nearby reads during a sorted index batch.
type indexReadBuffer struct {
	source io.ReaderAt
	buf    [32 * 1024]byte
	offset int64
	n      int
}

func (r *indexReadBuffer) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.offset && off-r.offset <= int64(r.n) && len(p) <= r.n-int(off-r.offset) {
		return copy(p, r.buf[off-r.offset:]), nil
	}
	if len(p) > len(r.buf) || off < 0 {
		return r.source.ReadAt(p, off)
	}
	var err error
	r.n, err = r.source.ReadAt(r.buf[:], off)
	r.offset = off
	n := copy(p, r.buf[:r.n])
	if n == len(p) {
		return n, nil
	}
	return n, err
}
