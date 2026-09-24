package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/catalog"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type fakeRunner struct {
	output func(domain.Command) (process.Result, error)
}

func TestFullManagerDiscoveryMergesGeneratedMetadata(t *testing.T) {
	m := MPM{Path: "mpm", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if reflect.DeepEqual(c.Args, []string{"--version"}) {
			return process.Result{Stdout: "mpm, version 8.0.1"}, nil
		}
		args := strings.Join(c.Args, " ")
		if !strings.Contains(args, "--table-format json managers --view all") || strings.Contains(args, "--brew") {
			t.Fatalf("discovery restricted to old core: %s", args)
		}
		return process.Result{Stdout: `{"npm":{"id":"npm","supported":true,"available":false,"executable":true,"fresh":false,"version":"11.6.2","cli_path":"/mise/node/bin/npm"},"go":{"id":"go","supported":true,"available":true,"executable":true,"fresh":true,"version":"1.26.0","cli_path":"/go/bin/go"},"uv":{"id":"uv","supported":true,"available":true,"executable":true,"fresh":true,"version":"0.11.0","cli_path":"/bin/uv"},"uvx":{"id":"uvx","supported":true,"available":true,"executable":true,"fresh":true,"version":"0.11.0","cli_path":"/bin/uv"},"winget":{"id":"winget","supported":false}}`}, nil
	}}}
	managers, err := m.Managers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Manager{}
	for _, v := range managers {
		byID[v.ID] = v
	}
	if len(managers) != 4 {
		t.Fatalf("managers: %+v", managers)
	}
	npm := byID["npm"]
	if npm.Status != "version unsupported" || npm.Requirement != ">=11.10.0" || !strings.Contains(npm.Reason, "min-release-age") {
		t.Fatal(npm)
	}
	goManager := byID["go"]
	if goManager.Scope != "global" || goManager.Supports("search") || goManager.Supports("upgrade") || !goManager.Supports("install") {
		t.Fatal(goManager)
	}
	if byID["uv-pip"].BackendID != "uv" || byID["uv-pip"].Scope != "environment" || byID["uvx"].Scope != "global" || !byID["uvx"].Supports("search") {
		t.Fatalf("uv collision: %+v", byID)
	}
	if len(Core) != len(catalog.All()) || !slices.Contains(Core, "gem") || !slices.Contains(Core, "rustup") {
		t.Fatal("Core not generated")
	}
}

func TestUnmaintainedInstalledManagerRemainsVisible(t *testing.T) {
	m := MPM{Path: "mpm", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if reflect.DeepEqual(c.Args, []string{"--version"}) {
			return process.Result{Stdout: "mpm, version 8.0.1"}, nil
		}
		if !strings.Contains(strings.Join(c.Args, " "), "managers --view all") {
			t.Fatal("unmaintained adapters hidden by discovery")
		}
		return process.Result{Stdout: `{"volta":{"id":"volta","supported":true,"available":true,"executable":true,"fresh":true,"version":"2.0.2","cli_path":"/tools/volta"},"winget":{"id":"winget","supported":false,"available":false}}`}, nil
	}}}
	managers, err := m.Managers(context.Background())
	if err != nil || len(managers) != 1 {
		t.Fatal(managers, err)
	}
	if managers[0].ID != "volta" || managers[0].Maintained || managers[0].Scope != "unknown" || !managers[0].Available {
		t.Fatal(managers[0])
	}
	if slices.Contains(catalog.DefaultIDs(), "volta") {
		t.Fatal("unmaintained adapter enabled by default")
	}
}

func TestUVPipWireIdentityAndScope(t *testing.T) {
	m := MPM{Path: "mpm", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if reflect.DeepEqual(c.Args, []string{"--version"}) {
			return process.Result{Stdout: "mpm, version 8.0.1"}, nil
		}
		if !strings.Contains(strings.Join(c.Args, " "), "--uv installed") {
			t.Fatalf("wrong backend flag: %v", c.Args)
		}
		return process.Result{Stdout: `{"uv":{"packages":[{"id":"httpx","installed_version":"1.0"}],"errors":[]}}`}, nil
	}}}
	s, err := m.Packages(context.Background(), "installed", "", "uv-pip")
	if err != nil || len(s.Packages) != 1 || s.Packages[0].Manager != "uv-pip" || s.Packages[0].Scope != "environment" {
		t.Fatal(s, err)
	}
	if got := Specifier("uv-pip", "httpx"); got != "pkg:uv/httpx" {
		t.Fatal(got)
	}
	if got := Specifier("uv", "httpx"); got != "pkg:uvx/httpx" {
		t.Fatal(got)
	}
	c, cleanup, err := m.Mutation(domain.ActionRequest{Manager: "uv-pip", Operation: "install", Package: "httpx"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.Contains(strings.Join(c.Args, " "), "--uv install -- pkg:uv/httpx") {
		t.Fatal(c.Args)
	}
}

func (f fakeRunner) Output(_ context.Context, c domain.Command) (process.Result, error) {
	return f.output(c)
}
func (f fakeRunner) Run(context.Context, domain.Command, io.Reader, io.Writer, io.Writer) error {
	return fmt.Errorf("unexpected mutation")
}
func TestDecodePartialManagerErrors(t *testing.T) {
	s, err := Decode([]byte(`{"brew":{"id":"brew","packages":[{"id":"jq","name":null,"installed_version":null}],"errors":[{"message":"query failed"}]}}`), "brew")
	if err != nil || len(s.Packages) != 1 || s.Packages[0].Version != "" || len(s.Issues) != 1 {
		t.Fatalf("lost partial data: %#v %v", s, err)
	}
	if _, err = Decode([]byte(`{}`), "brew"); err == nil {
		t.Fatal("omitted manager accepted")
	}
	if _, err = Decode([]byte(`[]`), "brew"); err == nil {
		t.Fatal("unverified array schema accepted")
	}
}
func TestWinGetRecognitionIsNotInstallerProof(t *testing.T) {
	s, err := Decode([]byte(`{"winget":{"packages":[{"id":"Git.Git","installed_version":"2"}]}}`), "winget")
	if err != nil || s.Packages[0].Evidence[0].Kind != "recognized" {
		t.Fatal(s, err)
	}
}

func TestGoBuildMetadataIsNotInstallerProof(t *testing.T) {
	s, err := Decode([]byte(`{"go":{"packages":[{"id":"example.org/cmd/tool","installed_version":"v1.0.0"}],"errors":[]}}`), "go")
	if err != nil || len(s.Packages) != 1 || s.Packages[0].Evidence[0].Kind != "recognized" || !strings.Contains(s.Packages[0].Evidence[0].Detail, "original installer is unknown") {
		t.Fatal(s, err)
	}
}

func TestReadOnlyQueriesDisableMiseAutoInstallation(t *testing.T) {
	original := map[string]string{"MISE_AUTO_INSTALL": "1", "MISE_NOT_FOUND_AUTO_INSTALL": "true", "GEM_HOME": "/chosen/gems", "GOTOOLCHAIN": "go1.29+auto"}
	queries := 0
	m := MPM{Path: "mpm", Env: original, Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		queries++
		if c.Env["MISE_AUTO_INSTALL"] != "0" || c.Env["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" || c.Env["GEM_HOME"] != "/chosen/gems" || c.Env["GOTOOLCHAIN"] != "local" {
			t.Fatalf("unsafe query environment: %+v", c.Env)
		}
		if reflect.DeepEqual(c.Args, []string{"--version"}) {
			return process.Result{Stdout: "mpm, version 8.0.1"}, nil
		}
		if !strings.Contains(strings.Join(c.Args, " "), "--timeout 5 --table-format json managers --view all") {
			t.Fatal(c.Args)
		}
		b, err := os.ReadFile(c.Args[1])
		if err != nil || !strings.Contains(string(b), `"version_cli_options":["version"]`) {
			t.Fatalf("Go probe not corrected: %s %v", b, err)
		}
		return process.Result{Stdout: `{"go":{"id":"go","supported":true,"available":true,"executable":true,"fresh":true,"version":"1.27.0","cli_path":"/go"},"npm":{"id":"npm","supported":true,"available":false,"executable":true,"fresh":false,"cli_path":"/slow/npm","errors":["Timed out after 5s."]}}`}, nil
	}}}
	rows, err := m.Managers(context.Background())
	if err != nil || len(rows) != 2 || queries != 2 {
		t.Fatal(rows, err, queries)
	}
	if original["MISE_AUTO_INSTALL"] != "1" {
		t.Fatal("caller environment was mutated")
	}
	mutation, cleanup, err := m.Mutation(domain.ActionRequest{Manager: "go", Operation: "install", Package: "example.com/tool"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if mutation.Env["GOTOOLCHAIN"] != "go1.29+auto" || mutation.Env["MISE_AUTO_INSTALL"] != "1" {
		t.Fatal("query isolation leaked into mutation", mutation.Env)
	}
	for _, row := range rows {
		if row.ID == "npm" && (row.Available || len(row.Errors) != 1) {
			t.Fatal("per-manager probe failure discarded", row)
		}
	}
}

func TestProbeTimeoutRespectsShortOuterDeadline(t *testing.T) {
	for _, tc := range []struct {
		outer time.Duration
		want  int
	}{{0, 5}, {30 * time.Second, 5}, {4 * time.Second, 2}, {time.Second, 1}} {
		m := MPM{Timeout: tc.outer}
		if got := m.probeTimeoutSeconds(); got != tc.want {
			t.Fatalf("%v: %d", tc.outer, got)
		}
	}
}

func TestNativeMiseReadsGuardAutoinstallWithoutChangingMutations(t *testing.T) {
	env := map[string]string{"MISE_AUTO_INSTALL": "true", "MISE_ENV": "work"}
	m := Mise{Path: "mise", Env: env, Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if c.Env["MISE_AUTO_INSTALL"] != "0" || c.Env["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" || c.Env["MISE_ENV"] != "work" || c.Env["GOTOOLCHAIN"] != "local" {
			t.Fatal(c.Env)
		}
		return process.Result{Stdout: "{}"}, nil
	}}}
	if _, err := m.output(context.Background(), "ls", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.globalOutput(context.Background(), "ls", "--global", "--json"); err != nil {
		t.Fatal(err)
	}
	if c := m.Command("install", "node@24"); c.Env["MISE_AUTO_INSTALL"] != "true" {
		t.Fatal("mutation environment was overridden", c.Env)
	}
}

func TestUVXEntrypointParserArtifactIsNotAPackage(t *testing.T) {
	s, err := Decode([]byte(`{"uvx":{"packages":[{"id":"visidata","installed_version":"3.3"},{"id":"-","installed_version":"isidata"},{"id":"-","installed_version":"d"}]}}`), "uvx")
	if err != nil || len(s.Packages) != 1 || s.Packages[0].ID != "visidata" {
		t.Fatal(s, err)
	}
}
func TestMPMContractAndTemporaryConfig(t *testing.T) {
	var configPath string
	r := fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if strings.Join(c.Args, " ") == "--version" {
			return process.Result{Stdout: "mpm, version 8.0.1"}, nil
		}
		if c.Args[0] != "--config" {
			t.Fatal("missing isolated config")
		}
		configPath = c.Args[1]
		b, err := os.ReadFile(configPath)
		if err != nil || string(b) != isolatedConfig {
			t.Fatalf("bad config %q %v", b, err)
		}
		args := strings.Join(c.Args, " ")
		if !strings.Contains(args, "--table-format json --brew installed") {
			t.Fatal(args)
		}
		if len(c.Unset) != 1 || c.Unset[0] != "MPM_*" {
			t.Fatal("mpm environment is not isolated")
		}
		return process.Result{Stdout: `{"brew":{"packages":[{"id":"jq","installed_version":"1.8"}],"errors":[]}}`}, nil
	}}
	m := MPM{Path: "mpm", Runner: r}
	s, err := m.Packages(context.Background(), "installed", "", "brew")
	if err != nil || len(s.Packages) != 1 {
		t.Fatal(s, err)
	}
	if _, err = os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatal("temporary config leaked")
	}
}
func TestRejectUntestedMPMVersion(t *testing.T) {
	m := MPM{Path: "mpm", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		return process.Result{Stdout: "mpm, version 9.0.0"}, nil
	}}}
	if err := m.Check(context.Background()); err == nil {
		t.Fatal("unverified version accepted")
	}
}

func TestLiteralNativePackageIDs(t *testing.T) {
	for _, c := range []struct{ manager, id, want string }{{"brew", "python@3.13", "pkg:brew/python%403.13"}, {"brew", "user/tap/foo@1", "pkg:brew/user/tap/foo%401"}, {"npm", "@scope/pkg", "pkg:npm/%40scope/pkg"}, {"npm", "JSONStream", "pkg:npm/JSONStream"}, {"uvx", "pycowsay", "pkg:uvx/pycowsay"}} {
		if got := Specifier(c.manager, c.id); got != c.want {
			t.Errorf("%s: %s != %s", c.id, got, c.want)
		}
	}
	var previewArgs []string
	m := MPM{Path: "mpm", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if len(c.Args) == 1 {
			return process.Result{Stdout: "mpm 8.0.1"}, nil
		}
		previewArgs = c.Args
		return process.Result{Stdout: "brew install python@3.13", Stderr: "Load configuration matching /random/path"}, nil
	}}}
	req := domain.ActionRequest{Manager: "brew", Operation: "install", Package: "python@3.13"}
	preview, err := m.Preview(context.Background(), req)
	if err != nil || preview != "brew install python@3.13" {
		t.Fatal(preview, err)
	}
	c, done, err := m.Mutation(req)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if previewArgs[len(previewArgs)-1] != c.Args[len(c.Args)-1] || c.Args[len(c.Args)-1] != "pkg:brew/python%403.13" {
		t.Fatal(previewArgs, c.Args)
	}
}
func TestMiseVersionsAndConfigurationSource(t *testing.T) {
	m := Mise{Path: "mise", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		args := strings.Join(c.Args, " ")
		switch args {
		case "ls --installed --json":
			return process.Result{Stdout: `{"node":[{"version":"20.0.0","install_path":"/tools/node/20"},{"version":"22.0.0","install_path":"/tools/node/22"}]}`}, nil
		case "ls --global --json":
			return process.Result{Stdout: `{"node":[{"version":"20.0.0"}]}`}, nil
		case "ls --current --json":
			return process.Result{Stdout: `{"node":[{"version":"22.0.0","source":{"type":"mise.toml","path":"/project/mise.toml"}}]}`}, nil
		}
		return process.Result{}, fmt.Errorf("unexpected %s", args)
	}}}
	s, err := m.Installed(context.Background())
	if err != nil || len(s.Packages) != 2 {
		t.Fatal(s, err)
	}
	for _, p := range s.Packages {
		if p.Version == "20.0.0" && (!p.Global || p.Active) {
			t.Fatal(p)
		}
		if p.Version == "22.0.0" && (!p.Active || p.Global || p.ConfigSource != "/project/mise.toml") {
			t.Fatal(p)
		}
	}
}
