// SPDX-License-Identifier: AGPL-3.0-only

package docscheck

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The platform templates (render.yaml, railway.json, deploy/fly) are only
// exercised by a real deploy. These tests keep them parseable and in step with
// the rest of the repository: same TimescaleDB image, same Dockerfile, same
// raw store path, health check on /readyz, only variables ohs knows.

const rawMount = "/var/lib/ohs/raw"

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// composeTimescaleImage is the pinned image of the compose postgres service.
func composeTimescaleImage(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*image:\s*(timescale/timescaledb:\S+@sha256:[0-9a-f]{64})\s*$`).
		FindSubmatch(repoFile(t, "deploy/docker-compose.yml"))
	if m == nil {
		t.Fatal("deploy/docker-compose.yml: no timescale/timescaledb image pinned by digest")
	}
	return string(m[1])
}

// envExampleVars are the OHS_ variables .env.example documents, commented
// out or not.
func envExampleVars(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^#?\s*(OHS_[A-Z0-9_]+)=`).FindAllSubmatch(repoFile(t, ".env.example"), -1) {
		out[string(m[1])] = true
	}
	return out
}

// dockerfileArgs are the ARG names of deploy/Dockerfile.
func dockerfileArgs(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^ARG ([A-Z_]+)`).FindAllSubmatch(repoFile(t, "deploy/Dockerfile"), -1) {
		out[string(m[1])] = true
	}
	return out
}

type renderEnv struct {
	Key           string `yaml:"key"`
	Value         string `yaml:"value"`
	GenerateValue bool   `yaml:"generateValue"`
	Sync          *bool  `yaml:"sync"`
	FromService   *struct {
		Type      string `yaml:"type"`
		Name      string `yaml:"name"`
		Property  string `yaml:"property"`
		EnvVarKey string `yaml:"envVarKey"`
	} `yaml:"fromService"`
}

type renderService struct {
	Type    string `yaml:"type"`
	Name    string `yaml:"name"`
	Runtime string `yaml:"runtime"`
	Image   *struct {
		URL string `yaml:"url"`
	} `yaml:"image"`
	DockerfilePath          string `yaml:"dockerfilePath"`
	DockerContext           string `yaml:"dockerContext"`
	HealthCheckPath         string `yaml:"healthCheckPath"`
	MaxShutdownDelaySeconds int    `yaml:"maxShutdownDelaySeconds"`
	Disk                    *struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
		SizeGB    int    `yaml:"sizeGB"`
	} `yaml:"disk"`
	EnvVars []renderEnv `yaml:"envVars"`
}

func TestRenderBlueprint(t *testing.T) {
	var bp struct {
		Services []renderService `yaml:"services"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(repoFile(t, "render.yaml")))
	dec.KnownFields(true) // a misspelt field fails here instead of being ignored by Render
	if err := dec.Decode(&bp); err != nil {
		t.Fatalf("render.yaml: %v", err)
	}
	byName := map[string]renderService{}
	for _, s := range bp.Services {
		byName[s.Name] = s
	}
	db, ok := byName["ohs-db"]
	if !ok || db.Type != "pserv" || db.Runtime != "image" || db.Image == nil {
		t.Fatalf("render.yaml: want a pserv ohs-db running an image, got %+v", db)
	}
	if want := "docker.io/" + composeTimescaleImage(t); db.Image.URL != want {
		t.Errorf("render.yaml: ohs-db image %q, want %q (the deploy/docker-compose.yml digest)", db.Image.URL, want)
	}
	if db.Disk == nil || db.Disk.MountPath != "/var/lib/postgresql/data" {
		t.Errorf("render.yaml: ohs-db needs a disk on /var/lib/postgresql/data")
	}

	web, ok := byName["ohs"]
	if !ok || web.Type != "web" || web.Runtime != "docker" {
		t.Fatalf("render.yaml: want a docker web service ohs, got %+v", web)
	}
	if web.DockerfilePath != "./deploy/Dockerfile" || web.DockerContext != "." {
		t.Errorf("render.yaml: ohs builds %q in %q, want ./deploy/Dockerfile in .", web.DockerfilePath, web.DockerContext)
	}
	if web.HealthCheckPath != "/readyz" {
		t.Errorf("render.yaml: health check %q, want /readyz", web.HealthCheckPath)
	}
	if web.MaxShutdownDelaySeconds < 30 {
		t.Errorf("render.yaml: maxShutdownDelaySeconds %d is shorter than the 25 s ohs drains for, plus margin", web.MaxShutdownDelaySeconds)
	}
	if web.Disk == nil || web.Disk.MountPath != rawMount {
		t.Errorf("render.yaml: ohs needs a disk on %s", rawMount)
	}

	known, args := envExampleVars(t), dockerfileArgs(t)
	env := map[string]renderEnv{}
	for _, e := range web.EnvVars {
		env[e.Key] = e
		switch {
		case strings.HasPrefix(e.Key, "OHS_"):
			if !known[e.Key] {
				t.Errorf("render.yaml: %s is not a variable .env.example documents", e.Key)
			}
		case strings.HasPrefix(e.Key, "PG"):
		default:
			if !args[e.Key] {
				t.Errorf("render.yaml: %s is neither an ohs variable nor an ARG of deploy/Dockerfile", e.Key)
			}
		}
	}
	if !env["OHS_ENCRYPTION_KEY"].GenerateValue {
		t.Error("render.yaml: OHS_ENCRYPTION_KEY should be generated (generateValue: true)")
	}
	if env["OHS_RAW_STORE"].Value != "file://"+rawMount {
		t.Errorf("render.yaml: OHS_RAW_STORE %q, want file://%s", env["OHS_RAW_STORE"].Value, rawMount)
	}
	if env["IMAGE_UID"].Value != "0" {
		t.Error("render.yaml: IMAGE_UID must be 0, the disk is owned by root")
	}
	if u := env["OHS_DATABASE_URL"].Value; !strings.HasPrefix(u, "postgres://ohs@/ohs?") {
		t.Errorf("render.yaml: OHS_DATABASE_URL %q: host and password come from PGHOST and PGPASSWORD", u)
	}
	if fs := env["PGHOST"].FromService; fs == nil || fs.Name != db.Name || fs.Type != db.Type || fs.Property != "host" {
		t.Errorf("render.yaml: PGHOST must be the host of %s", db.Name)
	}
	if fs := env["PGPASSWORD"].FromService; fs == nil || fs.Name != db.Name || fs.EnvVarKey != "POSTGRES_PASSWORD" {
		t.Errorf("render.yaml: PGPASSWORD must be POSTGRES_PASSWORD of %s", db.Name)
	}
	for _, e := range db.EnvVars {
		if e.Key == "POSTGRES_PASSWORD" && !e.GenerateValue {
			t.Error("render.yaml: POSTGRES_PASSWORD should be generated")
		}
	}
}

func TestRailwayConfig(t *testing.T) {
	var cfg struct {
		Schema string `json:"$schema"`
		Build  struct {
			Builder        string `json:"builder"`
			DockerfilePath string `json:"dockerfilePath"`
		} `json:"build"`
		Deploy struct {
			HealthcheckPath         string `json:"healthcheckPath"`
			HealthcheckTimeout      int    `json:"healthcheckTimeout"`
			RequiredMountPath       string `json:"requiredMountPath"`
			SleepApplication        bool   `json:"sleepApplication"`
			RestartPolicyType       string `json:"restartPolicyType"`
			RestartPolicyMaxRetries int    `json:"restartPolicyMaxRetries"`
			DrainingSeconds         int    `json:"drainingSeconds"`
		} `json:"deploy"`
	}
	dec := json.NewDecoder(bytes.NewReader(repoFile(t, "railway.json")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("railway.json: %v", err)
	}
	if cfg.Build.Builder != "DOCKERFILE" || cfg.Build.DockerfilePath != "deploy/Dockerfile" {
		t.Errorf("railway.json: build %+v, want DOCKERFILE deploy/Dockerfile", cfg.Build)
	}
	if cfg.Deploy.HealthcheckPath != "/readyz" || cfg.Deploy.RequiredMountPath != rawMount {
		t.Errorf("railway.json: deploy %+v", cfg.Deploy)
	}
	if cfg.Deploy.SleepApplication {
		t.Error("railway.json: background sync needs the service awake")
	}
	if cfg.Deploy.DrainingSeconds < 30 {
		t.Errorf("railway.json: drainingSeconds %d is shorter than the 25 s ohs drains for, plus margin", cfg.Deploy.DrainingSeconds)
	}
}

// tomlLines does a light syntax check of a TOML file (comments, [tables],
// [[arrays]] and key = value lines with a string, number or boolean value)
// and returns its lines prefixed by their table: "[mounts] destination = ...".
func tomlLines(t *testing.T, rel string) []string {
	t.Helper()
	var (
		out   []string
		table string
	)
	header := regexp.MustCompile(`^\[\[?([a-z_.]+)\]\]?$`)
	kv := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*("[^"\\]*"|-?[0-9]+|true|false)$`)
	for i, line := range strings.Split(string(repoFile(t, rel)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := header.FindStringSubmatch(line); m != nil {
			table = m[1]
			continue
		}
		m := kv.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%s:%d: not a TOML line this check understands: %q", rel, i+1, line)
			continue
		}
		out = append(out, "["+table+"] "+m[1]+" = "+m[2])
	}
	return out
}

func requireLines(t *testing.T, rel string, have []string, want ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, l := range have {
		set[l] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("%s: missing %s", rel, w)
		}
	}
}

func TestFlyConfig(t *testing.T) {
	const app = "deploy/fly/fly.toml"
	requireLines(t, app, tomlLines(t, app),
		`[build.args] IMAGE_UID = "0"`,
		`[env] OHS_RAW_STORE = "file://`+rawMount+`"`,
		`[mounts] destination = "`+rawMount+`"`,
		`[http_service] internal_port = 8080`,
		`[http_service] auto_stop_machines = "off"`,
		`[http_service.checks] path = "/readyz"`,
		`[] kill_timeout = 35`,
	)
	if !dockerfileArgs(t)["IMAGE_UID"] {
		t.Error("deploy/Dockerfile: IMAGE_UID is not an ARG, the Fly and Render builds set it")
	}

	const pg = "deploy/fly/postgres/fly.toml"
	requireLines(t, pg, tomlLines(t, pg),
		`[build] image = "`+composeTimescaleImage(t)+`"`,
		`[mounts] destination = "/var/lib/postgresql/data"`,
		`[env] PGDATA = "/var/lib/postgresql/data/pgdata"`,
	)
}
