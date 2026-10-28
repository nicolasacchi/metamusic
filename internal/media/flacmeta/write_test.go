package flacmeta_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

func TestBlockHeader(t *testing.T) {
	t.Parallel()
	if got := flacmeta.BlockHeader(flacmeta.BlockPicture, true, 0x010203); !bytes.Equal(got, []byte{0x86, 1, 2, 3}) {
		t.Fatalf("% x", got)
	}
	if got := flacmeta.BlockHeader(flacmeta.BlockSeekTable, false, 54); !bytes.Equal(got, []byte{3, 0, 0, 54}) {
		t.Fatalf("% x", got)
	}
}

func TestVorbisComment_Escapes(t *testing.T) {
	t.Parallel()
	c := []flacmeta.Comment{
		{Key: "TITLE", Value: "Zażółć gęślą · 夜"},
		{Key: "DESCRIPTION", Value: "a=b=c"},
		{Key: "EMPTY", Value: ""},
	}
	if err := flacmeta.ValidateComments(c); err != nil {
		t.Fatal(err)
	}
	b := flacmeta.VorbisComment("v1", c)
	// round trip through the reader: the payload parses back to exactly the entries
	file := append([]byte("fLaC"), flacmeta.BlockHeader(flacmeta.BlockStreamInfo, false, 34)...)
	si := make([]byte, 34)
	binary.BigEndian.PutUint64(si[10:], 48000<<44|1<<41|23<<36)
	file = append(file, si...)
	file = append(file, flacmeta.BlockHeader(flacmeta.BlockVorbisComment, true, len(b))...)
	file = append(file, b...)
	info, err := flacmeta.Read(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"TITLE=Zażółć gęślą · 夜", "DESCRIPTION=a=b=c", "EMPTY="}
	if diff := cmp.Diff(want, info.Comments); diff != "" || info.Vendor != "v1" {
		t.Fatal(diff, info.Vendor)
	}
	for _, bad := range []flacmeta.Comment{{Key: ""}, {Key: "A=B"}, {Key: "tab\t"}, {Key: "é"}, {Key: "K~"}, {Key: "K", Value: "a\xffb"}, {Key: "K", Value: "a\x00b"}} {
		if flacmeta.ValidateComments([]flacmeta.Comment{bad}) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPictureBlock_Golden(t *testing.T) {
	t.Parallel()
	got := flacmeta.PictureBlock(flacmeta.Picture{Type: 3, MIME: "image/png", Desc: "d", W: 512, H: 256, Depth: 8, Colors: 16, Data: []byte{9, 8, 7}})
	want := []byte{
		0, 0, 0, 3, 0, 0, 0, 9, 'i', 'm', 'a', 'g', 'e', '/', 'p', 'n', 'g', 0, 0, 0, 1, 'd',
		0, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0, 8, 0, 0, 0, 16, 0, 0, 0, 3, 9, 8, 7,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("% x", got)
	}
}
