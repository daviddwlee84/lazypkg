package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type fakeRunner struct {
	output func(domain.Command) (process.Result, error)
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
		if err != nil || string(b) != `{"mpm":{}}` {
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
