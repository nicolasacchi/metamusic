package objstore_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestNoSecretLiterals_Bulk: nothing in the bulk fallback sources holds a key-shaped literal. Fixtures are assembled
// from fragments at run time (the gitleaks rule and this test agree on the assignment shape).
func TestNoSecretLiterals_Bulk(t *testing.T) {
	assign := regexp.MustCompile(`(?i)R2_(SECRET_ACCESS_KEY|ACCESS_KEY_ID)\s*[=:]\s*\S{8,}`)
	awsKey := regexp.MustCompile(`AKIA[A-Z0-9]{16}`)
	awsSecret := regexp.MustCompile(`[A-Za-z0-9/+]{20,}EXAMPLEKEY`)
	var files []string
	for _, root := range []string{".", "../config"} {
		ents, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".go" && (root == "." || e.Name() == "fallback.go" || e.Name() == "fallback_test.go") {
				files = append(files, filepath.Join(root, e.Name()))
			}
		}
	}
	files = append(files, filepath.Join("objstoretest", "server.go"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, re := range []*regexp.Regexp{assign, awsKey, awsSecret} {
			if loc := re.FindIndex(b); loc != nil {
				t.Errorf("%s: secret-shaped literal at byte %d (%s)", f, loc[0], re)
			}
		}
	}
}
