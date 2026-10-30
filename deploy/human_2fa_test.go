package deploy

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The app enforces its own step-up only for local sessions; in proxy mode the second factor for /human/ comes from
// the edge alone (review finding S1). These tests pin the two halves of that edge contract in the files the repo
// ships: the Authelia rules example (a two_factor rule for ^/human/ that nothing weaker shadows) and the Traefik
// labels example (every router that can receive /human/ runs forward-auth). They read the shipped examples only;
// the operator's live Authelia/Traefik config is outside the repo and is not checked here.

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAutheliaRules_HumanIsTwoFactor(t *testing.T) {
	t.Parallel()
	lines := strings.Split(readFile(t, "authelia-rules.example.yml"), "\n")
	var def string
	type rule struct {
		policy    string
		resources []*regexp.Regexp
	}
	var rules []rule
	inRes := false
	for _, l := range lines {
		tr := strings.TrimSpace(l)
		if tr == "" || strings.HasPrefix(tr, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(tr, "default_policy:"):
			def = strings.Trim(strings.TrimSpace(strings.TrimPrefix(tr, "default_policy:")), `'"`)
		case strings.HasPrefix(tr, "- domain:"):
			rules = append(rules, rule{})
			inRes = false
		case strings.HasPrefix(tr, "policy:") && len(rules) > 0:
			rules[len(rules)-1].policy = strings.Trim(strings.TrimSpace(strings.TrimPrefix(tr, "policy:")), `'"`)
		case tr == "resources:":
			inRes = true
		case inRes && strings.HasPrefix(tr, "- "):
			re, err := regexp.Compile(strings.Trim(strings.TrimSpace(tr[2:]), `'"`))
			if err != nil {
				t.Fatalf("resource %q: %v", tr, err)
			}
			rules[len(rules)-1].resources = append(rules[len(rules)-1].resources, re)
		default:
			inRes = false
		}
	}
	if def != "deny" {
		t.Errorf("default_policy = %q, want deny", def)
	}
	// First match wins (a rule without resources matches every path). /human/ and a nested page must both land on two_factor.
	for _, p := range []string{"/human/", "/human/tokens", "/human/api/caps"} {
		got := ""
		for _, r := range rules {
			hit := len(r.resources) == 0
			for _, re := range r.resources {
				hit = hit || re.MatchString(p)
			}
			if hit {
				got = r.policy
				break
			}
		}
		if got != "two_factor" {
			t.Errorf("first matching rule for %s has policy %q, want two_factor", p, got)
		}
	}
}

var (
	ruleLabel = regexp.MustCompile(`traefik\.http\.routers\.([\w-]+)\.rule=(.*)$`)
	mwLabel   = regexp.MustCompile(`traefik\.http\.routers\.([\w-]+)\.middlewares=(.*)$`)
	pathCall  = regexp.MustCompile("(!?)(Path|PathPrefix)\\(`([^`]*)`\\)")
)

// canReceiveHuman is a conservative reading of a Traefik rule: it reports whether a request for /human/ could match it.
// Host and header matchers are assumed to match; only path matchers narrow.
func canReceiveHuman(rule string) bool {
	const target = "/human/"
	positive, positiveHit := false, false
	for _, m := range pathCall.FindAllStringSubmatch(rule, -1) {
		neg, kind, p := m[1] == "!", m[2], m[3]
		hit := (kind == "Path" && p == target) || (kind == "PathPrefix" && strings.HasPrefix(target, p))
		if neg {
			if hit {
				return false
			}
			continue
		}
		positive = true
		positiveHit = positiveHit || hit
	}
	return !positive || positiveHit
}

func TestTraefikLabels_HumanRoutersRunForwardAuth(t *testing.T) {
	t.Parallel()
	rules, mws := map[string]string{}, map[string]string{}
	for _, l := range strings.Split(readFile(t, "labels.example.yml"), "\n") {
		if m := ruleLabel.FindStringSubmatch(l); m != nil {
			rules[m[1]] = m[2]
		}
		if m := mwLabel.FindStringSubmatch(l); m != nil {
			mws[m[1]] = m[2]
		}
	}
	if len(rules) == 0 {
		t.Fatal("no router rules found in labels.example.yml")
	}
	if !canReceiveHuman(rules["mm-ui"]) {
		t.Fatalf("mm-ui rule %q should receive /human/", rules["mm-ui"])
	}
	for name, rule := range rules {
		if !canReceiveHuman(rule) {
			continue
		}
		var hasAuth bool
		for _, mw := range strings.Split(mws[name], ",") {
			hasAuth = hasAuth || strings.TrimSpace(mw) == "authelia@docker"
		}
		if !hasAuth {
			t.Errorf("router %s can receive /human/ but has no authelia@docker middleware (middlewares=%q)", name, mws[name])
		}
	}
}

func TestCanReceiveHuman(t *testing.T) {
	t.Parallel()
	for rule, want := range map[string]bool{
		"Host(`h`)":                        true,
		"Host(`h`) && PathPrefix(`/api/`)": false,
		"Host(`h`) && (Path(`/mcp`) || PathPrefix(`/mcp/`))":     false,
		"Host(`h`) && PathPrefix(`/human/`)":                     true,
		"Host(`h`) && PathPrefix(`/`)":                           true,
		"Host(`h`) && !Path(`/human`) && !PathPrefix(`/human/`)": false,
	} {
		if got := canReceiveHuman(rule); got != want {
			t.Errorf("canReceiveHuman(%s) = %v, want %v", rule, got, want)
		}
	}
}
