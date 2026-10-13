package importer

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPrivacy_M2c: reference and lossy takes leak neither an absolute path nor a title into report
// text and issues, and control characters in a lossy file name stay escaped.
func TestPrivacy_M2c(t *testing.T) {
	const sent = "zqxM2cTitleSentinel"
	e := newEnv(t)
	e.flac("music/tagged.flac", "ft", "TITLE="+sent)
	e.mp3("lossy/"+"evil\x1b[31m\u202egnp.mp3", 3)
	e.mp3("lossy/titled.mp3", 4)
	e.mp3("lossy/fake_x.mp3", 5)
	e.mapping("lossy/titled.mp3,reference,," + sent + "mapped,\n")
	for i, flags := range []func(*Options){withRefFlags(false, false), withRefFlags(true, true)} {
		_, inv := e.inventory(flags)
		var text strings.Builder
		inv.Report.FormatText(&text)
		text.WriteString(mappingTemplate(t, inv))
		raw, err := json.Marshal(inv.Report.Warnings)
		if err != nil {
			t.Fatal(err)
		}
		errs, _ := json.Marshal(inv.Report.Errors)
		for name, s := range map[string]string{"text": text.String(), "warnings": string(raw), "errors": string(errs)} {
			bad := []string{e.inboxDir, e.dataDir}
			if name == "text" { // JSON issues carry the path as M1 always did; only terminal text is escaped
				bad = append(bad, "\x1b", "\u202e")
			}
			for _, bad := range bad {
				if strings.Contains(s, bad) {
					t.Errorf("%s leaks %q:\n%s", name, bad, s)
				}
			}
			if strings.Contains(s, sent) {
				t.Errorf("%s carries a title:\n%s", name, s)
			}
		}
		if i == 0 && !strings.Contains(text.String(), `\x1b`) {
			t.Errorf("escaped control character missing:\n%s", text.String())
		}
	}
}
