package canonjson

import (
	"strings"
	"testing"
)

func TestCanonical(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"sorted keys and exact numbers", `{"b":1,"a":[2,{"d":1e3,"c":1.0}]}`, `{"a":[2,{"c":1.0,"d":1e3}],"b":1}`},
		{"no html escape", `{"k":"<&>"}`, `{"k":"<&>"}`},
		{"whitespace dropped", " {\n \"a\" : [ 1 , 2 ] } ", `{"a":[1,2]}`},
		{"unicode kept", `{"é":"città"}`, `{"é":"città"}`},
		{"big number exact", `[12345678901234567890.5e-3]`, `[12345678901234567890.5e-3]`},
		{"escapes reencoded", `"A\n"`, `"A\n"`},
		{"scalars", `null`, `null`},
	} {
		got, err := Canonical([]byte(tc.in))
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	deep := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	if _, err := Canonical([]byte(deep(64))); err != nil {
		t.Errorf("depth 64: %v", err)
	}
	for name, in := range map[string]string{
		"duplicate key":    `{"a":1,"a":2}`,
		"nested dup":       `[{"a":1,"b":{"x":1,"x":1}}]`,
		"depth 65":         deep(65),
		"invalid utf8":     "{\"a\":\"\xff\"}",
		"invalid json":     `{"a":`,
		"trailing data":    `{} {}`,
		"empty":            ``,
		"invalid utf8 key": "{\"\xff\":1}",
	} {
		if got, err := Canonical([]byte(in)); err == nil {
			t.Errorf("%s: want error, got %q", name, got)
		}
	}
}

func TestCanonical_Idempotent(t *testing.T) {
	a, _ := Canonical([]byte(`{"z":[1,{"b":2,"a":1}],"a":"x"}`))
	b, err := Canonical(a)
	if err != nil || string(a) != string(b) {
		t.Fatalf("not idempotent: %q vs %q (%v)", a, b, err)
	}
}
