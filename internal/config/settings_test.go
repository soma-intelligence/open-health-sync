// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var bootstrapNames = []string{
	"OHS_DATABASE_URL", "OHS_ENCRYPTION_KEY", "OHS_ENCRYPTION_KEY_FILE", "OHS_PREVIOUS_ENCRYPTION_KEYS",
	"OHS_VAULT_KEY_PROVIDER", "OHS_VAULT_ROOT_KEY", "OHS_VAULT_ROOT_KEY_FILE", "OHS_VAULT_ROOT_KEY_PREVIOUS",
	"OHS_VAULT_HASHICORP_ADDR", "OHS_VAULT_HASHICORP_MOUNT", "OHS_VAULT_HASHICORP_KEY",
	"OHS_VAULT_HASHICORP_TOKEN_FILE", "OHS_VAULT_AZURE_KEY_ID", "OHS_VAULT_AWS_KEY_ARN",
	"OHS_VAULT_CACHE_USERS", "OHS_VAULT_CACHE_TTL",
	"OHS_RAW_STORE", "OHS_LISTEN", "OHS_OPS_LISTEN", "OHS_METRICS_LISTEN", "OHS_ROLES", "OHS_CONSOLE",
	"OHS_SETUP_TOKEN", "OHS_SETUP_TOKEN_FILE", "OHS_BOOTSTRAP_TENANT",
}

var liveNames = []string{
	"OHS_LOG_LEVEL", "OHS_RETENTION_DAYS", "OHS_AUDIT_RETENTION_DAYS", "OHS_TRACKING_RETENTION_DAYS",
	"OHS_CONSENT_REQUIRED", "OHS_BACKFILL_DAYS", "OHS_WEBHOOK_PAUSE_AFTER", "OHS_WEBHOOK_ROTATION_GRACE_HOURS",
	"OHS_RATE_LIMIT_API", "OHS_RATE_LIMIT_AUTH_FAILURES", "OHS_RATE_LIMIT_INBOUND",
	"OHS_RATE_LIMIT_INBOUND_PROVIDER", "OHS_RATE_LIMIT_OAUTH",
}

var secretNames = []string{
	"OHS_DATABASE_URL", "OHS_ENCRYPTION_KEY", "OHS_PREVIOUS_ENCRYPTION_KEYS", "OHS_VAULT_ROOT_KEY",
	"OHS_VAULT_ROOT_KEY_PREVIOUS", "OHS_SETUP_TOKEN", "OHS_METRICS_TOKEN",
}

var ohsName = regexp.MustCompile(`^OHS_[A-Z0-9_]+$`)

// Every OHS_ name Load reads is in the registry, and every registry entry is
// read by Load: settings.go is left out of the scan, it is what is checked.
func TestRegistryMatchesLoad(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	secretCalls := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "settings.go" {
			continue
		}
		tree, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(n.Value); err == nil && ohsName.MatchString(s) && !notServer[s] {
					found[s] = true
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "secret" && len(n.Args) > 1 {
					if lit, ok := n.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							found[s+"_FILE"] = true
							secretCalls[s] = true
						}
					}
				}
			}
			return true
		})
	}
	if !reflect.DeepEqual(secretCalls, fileTwin) {
		t.Errorf("fileTwin %v must equal the settings Load reads through secret() %v", fileTwin, secretCalls)
	}
	reg := map[string]bool{}
	for _, s := range Registry() {
		reg[s.Name] = true
	}
	for n := range found {
		if !reg[n] {
			t.Errorf("Load reads %s, which has no registry entry", n)
		}
	}
	for n := range reg {
		if !found[n] {
			t.Errorf("registry entry %s is not read by Load", n)
		}
	}
}

func TestRegistryShape(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Registry() {
		if seen[s.Name] {
			t.Errorf("%s is declared twice", s.Name)
		}
		seen[s.Name] = true
		if !slices.Contains(Sections, s.Section) {
			t.Errorf("%s: section %q is unknown", s.Name, s.Section)
		}
		want := ApplyRestart
		switch {
		case slices.Contains(bootstrapNames, s.Name):
			want = ApplyBootstrap
		case slices.Contains(liveNames, s.Name):
			want = ApplyLive
		}
		if s.Apply != want {
			t.Errorf("%s: apply %q, want %q", s.Name, s.Apply, want)
		}
		if (s.Min != nil || s.Max != nil) && s.Type != TypeInt {
			t.Errorf("%s: Min or Max on a %s", s.Name, s.Type)
		}
		if (len(s.Values) > 0) != (s.Type == TypeEnum) {
			t.Errorf("%s: Values %v on a %s", s.Name, s.Values, s.Type)
		}
		if s.Secret != slices.Contains(secretNames, s.Name) {
			t.Errorf("%s: Secret is %v", s.Name, s.Secret)
		}
		if s.Label == "" || s.Help == "" || s.Validate == nil || s.Type == "" {
			t.Errorf("%s: label, help, type or validator missing", s.Name)
		}
		if s.Default != "" && s.DefaultNote != "" {
			t.Errorf("%s: Default and DefaultNote both set", s.Name)
		}
	}
	for _, n := range append(slices.Clone(bootstrapNames), liveNames...) {
		if !seen[n] {
			t.Errorf("%s is not in the registry", n)
		}
	}
	if len(bootstrapNames) != 25 || len(liveNames) != 13 {
		t.Fatalf("lists: %d bootstrap, %d live", len(bootstrapNames), len(liveNames))
	}
	// Entries follow the order of Sections, each section in one run.
	last := -1
	for _, s := range Registry() {
		i := slices.Index(Sections, s.Section)
		if i < last {
			t.Errorf("%s: section %s comes after a later one", s.Name, s.Section)
		}
		last = i
	}
	if len(Sections) != 14 {
		t.Errorf("%d sections", len(Sections))
	}
}

// samples are offered to every setting, with its Max+1 when it has one.
var samples = []string{"0", "-1", "1", "abc", "30/120", "0.5/1", "true", "maybe", "10m", "https://x.example", "10.0.0.0/8", " 5"}

func settingSamples(s Setting) []string {
	out := slices.Clone(samples)
	if s.Max != nil {
		out = append(out, strconv.FormatInt(*s.Max+1, 10))
	}
	return out
}

func TestValidateAgreesWithLoad(t *testing.T) {
	for _, s := range Registry() {
		if s.Apply == ApplyBootstrap {
			continue
		}
		for _, v := range settingSamples(s) {
			env := base()
			env[s.Name] = v
			_, err := load(t, env)
			verr := s.Validate(v)
			if (verr != nil) != (err != nil) {
				t.Errorf("%s=%q: Validate %v, Load %v", s.Name, v, verr, err)
				continue
			}
			if err != nil && verr.Error() != err.Error() && !strings.HasPrefix(err.Error(), s.Name) {
				t.Errorf("%s=%q: messages differ: %q and %q", s.Name, v, verr, err)
			}
		}
	}
}

func TestLoadErrorsStartWithTheName(t *testing.T) {
	for _, s := range Registry() {
		if s.Apply == ApplyBootstrap {
			continue
		}
		for _, v := range settingSamples(s) {
			env := base()
			env[s.Name] = v
			if _, err := load(t, env); err != nil && !strings.HasPrefix(err.Error(), s.Name) {
				t.Errorf("%s=%q: error %q does not start with the name", s.Name, v, err)
			}
			if err := s.Validate(v); err != nil && !strings.HasPrefix(err.Error(), s.Name) {
				t.Errorf("%s=%q: Validate error %q does not start with the name", s.Name, v, err)
			}
		}
	}
}

func TestStackedPrecedence(t *testing.T) {
	const name = "OHS_RETENTION_DAYS"
	look := func(env map[string]string, stored map[string]string, n string) (string, bool) {
		return Stacked(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, stored)(n)
	}
	if v, ok := look(map[string]string{name: "7"}, map[string]string{name: "9"}, name); v != "7" || !ok {
		t.Errorf("env over stored: %q %v", v, ok)
	}
	if v, ok := look(map[string]string{name: ""}, map[string]string{name: "9"}, name); v != "9" || !ok {
		t.Errorf("empty env falls to stored: %q %v", v, ok)
	}
	if v, ok := look(nil, map[string]string{name: "9"}, name); v != "9" || !ok {
		t.Errorf("stored alone: %q %v", v, ok)
	}
	if v, ok := look(nil, nil, name); v != "" || ok {
		t.Errorf("nothing set: %q %v", v, ok)
	}
	const key = "OHS_SETUP_TOKEN"
	if v, ok := look(map[string]string{key + "_FILE": "/run/x"}, map[string]string{key: "stored-token-0123456789"}, key); ok {
		t.Errorf("_FILE in env must hide the stored value, got %q", v)
	}
	if !EnvSet(func(k string) (string, bool) { return "/run/x", k == key+"_FILE" }, key) {
		t.Error("EnvSet misses the _FILE twin")
	}
	if EnvSet(func(k string) (string, bool) { return "/run/x", k == "OHS_METRICS_TOKEN_FILE" }, "OHS_METRICS_TOKEN") {
		t.Error("a _FILE twin Load never reads must not count as set")
	}
	if EnvSet(func(k string) (string, bool) { return "", true }, key) {
		t.Error("EnvSet counts an empty value")
	}
	if v, ok := look(map[string]string{"OHS_WITHINGS_CLIENT_ID": "e"}, map[string]string{"OHS_WITHINGS_CLIENT_ID": "s"}, "OHS_WITHINGS_CLIENT_ID"); v != "e" || !ok {
		t.Errorf("a name outside the registry goes to env: %q %v", v, ok)
	}
	if v, ok := look(nil, map[string]string{"OHS_WITHINGS_CLIENT_ID": "s"}, "OHS_WITHINGS_CLIENT_ID"); ok {
		t.Errorf("a name outside the registry is never stored: %q", v)
	}
	// A bootstrap setting is never read from the store.
	if v, ok := look(nil, map[string]string{"OHS_LISTEN": ":1"}, "OHS_LISTEN"); ok {
		t.Errorf("bootstrap from the store: %q", v)
	}
	// Stacked feeds Load: the stored value applies, the environment wins.
	c, err := Load(Stacked(func(k string) (string, bool) { v, ok := base()[k]; return v, ok }, map[string]string{name: "30"}), nil)
	if err != nil || c.RetentionDays != 30 {
		t.Errorf("Load over a stored value: %d %v", c.RetentionDays, err)
	}
}

var (
	envLine     = regexp.MustCompile(`^(?:# )?(OHS_[A-Z0-9_]+)=(.*)$`)
	defaultNote = regexp.MustCompile(`\(default ([^) ;]+)`)
)

func TestEnvExampleNamesTheRegistry(t *testing.T) {
	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	reg := map[string]Setting{}
	for _, s := range Registry() {
		reg[s.Name] = s
	}
	named := map[string]bool{}
	for _, l := range lines {
		if m := envLine.FindStringSubmatch(l); m != nil {
			named[m[1]] = true
		}
	}
	for n := range reg {
		if !named[n] {
			t.Errorf(".env.example does not name %s", n)
		}
	}
	for n := range named {
		if _, ok := reg[n]; ok || notServer[n] {
			continue
		}
		if _, _, ok := splitProviderKey(strings.TrimPrefix(n, "OHS_")); !ok {
			t.Errorf(".env.example names %s, which is neither a setting nor a provider key", n)
		}
	}
	for i, l := range lines {
		m := envLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		s, ok := reg[m[1]]
		if !ok || s.Default == "" {
			continue
		}
		if m[2] != "" && m[2] != s.Default {
			t.Errorf("%s: example value %q, default %q", s.Name, m[2], s.Default)
		}
		// The comment block above, when this is the only variable under it.
		first := i
		for first > 0 && envLine.MatchString(lines[first-1]) {
			first--
		}
		if first != i || (i+1 < len(lines) && envLine.MatchString(lines[i+1])) {
			continue
		}
		var block []string
		for j := first - 1; j >= 0 && strings.HasPrefix(lines[j], "#") && !envLine.MatchString(lines[j]); j-- {
			block = append(block, lines[j])
		}
		if ms := defaultNote.FindAllStringSubmatch(strings.Join(block, " "), -1); len(ms) == 1 {
			if got := strings.TrimRight(ms[0][1], ",.:"); got != s.Default {
				t.Errorf("%s: the comment says default %q, the registry %q", s.Name, got, s.Default)
			}
		}
	}
}

// Load gives the same Config whether a setting is unset or set to its Default.
func TestDefaultsAreWhatLoadUses(t *testing.T) {
	norm := func(c Config) Config { c.EncryptionKeySource = ""; return c }
	want, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Registry() {
		if s.Default == "" {
			continue
		}
		env := base()
		env[s.Name] = s.Default
		got, err := load(t, env)
		if err != nil {
			t.Errorf("%s=%q: %v", s.Name, s.Default, err)
			continue
		}
		if !reflect.DeepEqual(norm(got), norm(want)) {
			t.Errorf("%s: Load differs when set to its default %q", s.Name, s.Default)
		}
	}
}
