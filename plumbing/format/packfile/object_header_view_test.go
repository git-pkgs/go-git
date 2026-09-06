package packfile

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	gogitbinary "github.com/go-git/go-git/v6/utils/binary"
)

func TestReadObjectHeaderBytesMatchesScanner(t *testing.T) {
	t.Parallel()

	for _, objectIDSize := range []int{format.SHA1Size, format.SHA256Size} {
		var ref bytes.Buffer
		writeTestObjectHeader(&ref, plumbing.REFDeltaObject, (1<<60)-1)
		ref.Write(bytes.Repeat([]byte{0x5a}, objectIDSize))

		var ofs bytes.Buffer
		writeTestObjectHeader(&ofs, plumbing.OFSDeltaObject, 127)
		require.NoError(t, gogitbinary.WriteVariableWidthInt(&ofs, 42))

		var blob bytes.Buffer
		writeTestObjectHeader(&blob, plumbing.BlobObject, (1<<60)-1)

		for _, data := range [][]byte{blob.Bytes(), ref.Bytes(), ofs.Bytes()} {
			scannerOpts := []ScannerOption(nil)
			if objectIDSize == format.SHA256Size {
				scannerOpts = append(scannerOpts, WithSHA256())
			}
			scanner := NewScanner(bytes.NewReader(data), scannerOpts...)
			scanner.scannerReader.offset = 100
			want, err := scanner.readObjectHeader()
			require.NoError(t, err)

			got, err := readObjectHeaderBytes(data, 100, objectIDSize)
			require.NoError(t, err)
			require.Equal(t, want, got)
		}
	}
}
