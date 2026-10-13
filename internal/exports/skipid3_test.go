package exports

import (
	"bufio"
	"bytes"
	"io"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/mp3info"
)

func TestSkipID3_InvalidSizeIsNotATag(t *testing.T) {
	t.Parallel()
	data := []byte{'I', 'D', '3', 4, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF}
	for range 3 {
		frame := make([]byte, 417)
		copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
		data = append(data, frame...)
	}
	if _, err := mp3info.Parse(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("mp3info.Parse rejects the fixture: %v", err)
	}
	br := bufio.NewReader(bytes.NewReader(data))
	if err := skipID3(br); err != nil {
		t.Fatalf("skipID3: %v", err)
	}
	got, err := io.ReadAll(br)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("remaining stream differs from input (len %d vs %d, err %v)", len(got), len(data), err)
	}
}
