package export

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

func enc(t *testing.T, id, style string, s Style) []byte {
	t.Helper()
	return EncodeRequest(yue2.Request{ID: id, Style: style, Lyrics: "caffè", Cot: "full", Seed: 1}, s)
}

func TestDetectStyle(t *testing.T) {
	sorted := Style{ItemSep: ", ", KeySep: ": "}
	design := Style{KeyOrder: designOrder, ItemSep: ",", KeySep: ":", EnsureASCII: true}
	lines := func(s Style, n int) [][]byte {
		var out [][]byte
		for i := 0; i < n; i++ {
			out = append(out, enc(t, "a", "x", s))
		}
		return out
	}
	t.Run("sorted python raw", func(t *testing.T) {
		got, st := DetectStyle(lines(sorted, 3))
		if !reflect.DeepEqual(got, sorted) || !st.AllSorted || st.Scanned != 3 {
			t.Fatalf("%+v %+v", got, st)
		}
	})
	t.Run("design compact escaped", func(t *testing.T) {
		got, _ := DetectStyle(lines(design, 3))
		if !reflect.DeepEqual(got, design) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("majority wins", func(t *testing.T) {
		raws := append(lines(design, 3), lines(sorted, 2)...)
		got, _ := DetectStyle(raws)
		if !reflect.DeepEqual(got, design) {
			t.Fatalf("%+v", got)
		}
		raws = append(lines(sorted, 3), lines(design, 2)...)
		got, _ = DetectStyle(raws)
		if !reflect.DeepEqual(got, sorted) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("ascii lines abstain", func(t *testing.T) {
		r := yue2.Request{ID: "a", Style: "x", Lyrics: "plain", Cot: "full", Seed: 1}
		got, st := DetectStyle([][]byte{EncodeRequest(r, Style{ItemSep: ",", KeySep: ":", EnsureASCII: true})})
		if got.EnsureASCII || st.EscapeVotes != 0 || st.RawVotes != 0 || got.ItemSep != "," {
			t.Fatalf("%+v %+v", got, st)
		}
	})
	t.Run("no data", func(t *testing.T) {
		got, st := DetectStyle(nil)
		if !reflect.DeepEqual(got, DefaultStyle()) || st.Lines != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("ties default", func(t *testing.T) {
		raws := append(lines(sorted, 1), lines(Style{ItemSep: ",", KeySep: ":", EnsureASCII: true}, 1)...)
		got, _ := DetectStyle(raws)
		if !reflect.DeepEqual(got, DefaultStyle()) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("unparseable lines ignored", func(t *testing.T) {
		raws := [][]byte{[]byte("nope"), []byte(`{"a" 1}`), []byte(`{"a":1`), []byte(`{`), []byte(`{}`), []byte(`{"a":1,}`)}
		raws = append(raws, lines(design, 1)...)
		got, st := DetectStyle(raws)
		if !reflect.DeepEqual(got, design) || st.Lines != 7 {
			t.Fatalf("%+v %+v", got, st)
		}
	})
	t.Run("nested values and escapes", func(t *testing.T) {
		raw := []byte(`{"b": {"x": [1, "}", {"y": "\u00e8"}]}, "a\"k": "v\\", "n": null, "t": true}`)
		sc := scanLine(raw)
		if !sc.ok || !reflect.DeepEqual(sc.keys, []string{"b", `a"k`, "n", "t"}) || !sc.escape || sc.raw {
			t.Fatalf("%+v", sc)
		}
		if strings.Join(sc.itemSeps, "|") != ", |, |, " {
			t.Fatalf("%q", sc.itemSeps)
		}
	})
	t.Run("most frequent order", func(t *testing.T) {
		a := []byte(`{"id": 1, "style": 2, "abc": 3}`)
		b := []byte(`{"style": 1, "id": 2}`)
		got, st := DetectStyle([][]byte{a, a, b})
		if !reflect.DeepEqual(got.KeyOrder, []string{"id", "style", "abc"}) || st.AllSorted || st.DistinctKeyOrders != 2 {
			t.Fatalf("%+v %+v", got, st)
		}
	})
}
