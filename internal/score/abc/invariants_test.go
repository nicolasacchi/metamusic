package abc

import (
	"slices"
	"strings"
	"testing"
)

// invBase is the tune most invariant rows edit by textual replacement.
const invBase = `X:1
T:t
M:4/4
L:1/4
Q:1/4=100
K:C
V:1 name="vocal"
% verse
"C"CDEF | GABc | cBAG | FEDC |
% chorus
"G"GGGG | AAAA | BBBB | cccc |
V:2 name="bass"
% verse
C,4 | G,,4 | A,,4 | F,,4 |
% chorus
G,,4 | A,,4 | B,,4 | C,4 |
`

const invPickup = `X:1
M:4/4
L:1/4
Q:1/4=100
K:C
V:1
C | DEFG | ABcd | e |
`

func swap(old, nw string) func(string) string {
	return func(s string) string {
		if !strings.Contains(s, old) {
			panic("missing " + old)
		}
		return strings.Replace(s, old, nw, 1)
	}
}

func TestCheckInvariants_Table(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name  string
		base  string
		edit  func(string) string
		allow Allow
		want  []string
	}{
		{"identical", invBase, func(s string) string { return s }, Allow{}, nil},
		{"same length bar replaced", invBase, swap("GABc", "cdef"), Allow{}, nil},
		{"bar too long", invBase, swap("GABc", "GABcc"), Allow{}, []string{"bar_length"}},
		{"bar too short in the middle", invBase, swap("cBAG", "cBA"), Allow{}, []string{"bar_length"}},
		{"last bar shortened", invBase, swap("| C,4 |", "| C,2 |"), Allow{}, []string{"bar_length"}},
		{"bar removed", invBase, swap("GABc | ", ""), Allow{}, []string{"bar_count_changed", "sections_changed"}},
		{"bar added", invBase, swap("GABc |", "GABc | GABc |"), Allow{}, []string{"bar_count_changed", "sections_changed"}},
		{"voice renamed", invBase, swap(`name="bass"`, `name="low"`), Allow{}, []string{"voices_changed"}},
		{"voice id changed", invBase, swap("V:2 ", "V:3 "), Allow{}, []string{"voices_changed"}},
		{"voice removed", invBase, func(s string) string { return s[:strings.Index(s, "V:2")] }, Allow{}, []string{"voices_changed"}},
		{"meter changed", invBase, swap("M:4/4", "M:3/4"), Allow{}, []string{"header_changed"}},
		{"unit changed", invBase, swap("L:1/4", "L:1/8"), Allow{}, []string{"header_changed"}},
		{"key changed", invBase, swap("K:C", "K:D"), Allow{}, []string{"header_changed"}},
		{"tempo changed", invBase, swap("Q:1/4=100", "Q:1/4=90"), Allow{}, []string{"header_changed"}},
		{"tempo changed and allowed", invBase, swap("Q:1/4=100", "Q:1/4=90"), Allow{Tempo: true}, nil},
		{"tempo removed", invBase, swap("Q:1/4=100\n", ""), Allow{}, []string{"header_changed"}},
		{"section relabelled", invBase, swap("% verse", "% bridge"), Allow{}, []string{"sections_changed"}},
		{"section moved", invBase, swap("% chorus\n\"G\"GGGG |", "\"G\"GGGG |\n% chorus\n"), Allow{}, []string{"sections_changed"}},
		{"repeat in an edited bar", invBase, swap("| GABc |", "|: GABc :|"), Allow{}, []string{"unsupported_feature"}},
		{"inline field in an edited bar", invBase, swap("GABc", "[K:D]GABc"), Allow{}, []string{"unsupported_feature"}},
		{"volta in an edited bar", invBase, swap("| GABc |", "| [1 GABc |"), Allow{}, []string{"unsupported_feature"}},
		{"several violations are all listed", invBase, func(s string) string { return swap("M:4/4", "M:2/4")(swap("GABc | ", "")(s)) }, Allow{},
			[]string{"bar_count_changed", "header_changed", "sections_changed"}},
		{"pickup bar edited keeping its length", invPickup, swap("C |", "D |"), Allow{}, nil},
		{"pickup bar edited changing its length", invPickup, swap("C |", "CD |"), Allow{}, []string{"bar_length"}},
		{"short last bar kept", invPickup, swap("e |", "f |"), Allow{}, nil},
		{"short middle bar", invPickup, swap("ABcd", "ABc"), Allow{}, []string{"bar_length"}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			base := mustParse(t, r.base, DialectYuE2)
			ed := mustParse(t, r.edit(r.base), DialectYuE2)
			var got []string
			for _, v := range CheckInvariants(base, ed, r.allow) {
				got = append(got, v.Code)
			}
			slices.Sort(got)
			got = slices.Compact(got)
			want := slices.Clone(r.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("codes %v, want %v", got, want)
			}
		})
	}
}

func TestCheckInvariants_Locates(t *testing.T) {
	t.Parallel()
	base := mustParse(t, invBase, DialectYuE2)
	ed := mustParse(t, swap("cBAG", "cBA")(invBase), DialectYuE2)
	vs := CheckInvariants(base, ed, Allow{})
	if len(vs) != 1 || vs[0].Voice != "1" || vs[0].Bar != 2 || vs[0].Msg == "" {
		t.Fatalf("violations %+v", vs)
	}
}

func TestCheckInvariants_NilSafe(t *testing.T) {
	t.Parallel()
	b := mustParse(t, invBase, DialectYuE2)
	if vs := CheckInvariants(nil, b, Allow{}); len(vs) == 0 {
		t.Fatal("nil base must be a violation")
	}
	if vs := CheckInvariants(b, nil, Allow{}); len(vs) == 0 {
		t.Fatal("nil edited must be a violation")
	}
}
