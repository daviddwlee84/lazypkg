package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type fakeRunner struct {
	mu       sync.Mutex
	output   map[string]string
	failures map[string]error
	calls    []domain.Command
}

func commandKey(path string, args ...string) string { return path + " " + strings.Join(args, " ") }
func (r *fakeRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	key := commandKey(c.Path, c.Args...)
	out, ok := r.output[key]
	if err := r.failures[key]; err != nil {
		return process.Result{Stdout: out}, err
	}
	if !ok {
		return process.Result{}, errors.New("unexpected query: " + key)
	}
	return process.Result{Stdout: out}, nil
}
func (r *fakeRunner) Run(context.Context, domain.Command, io.Reader, io.Writer, io.Writer) error {
	panic("diagnostics must not perform actions")
}
func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
func linkFile(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}
func findExecutable(t *testing.T, r domain.DiagnosticReport, path string) domain.Executable {
	t.Helper()
	for _, e := range r.Executables {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("missing executable %s: %#v", path, r.Executables)
	return domain.Executable{}
}

// Filesystem-backed fixtures must use the host's executable and PATH rules.
// Setting GOOS to linux cannot give Windows files Unix execute bits or make a
// Windows drive letter safe inside a colon-separated PATH.
func nativeExecutable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func nativePATH(dirs ...string) string { return strings.Join(dirs, string(os.PathListSeparator)) }

func nativeTestEngine(r process.Runner, path, dir string) *Engine {
	return &Engine{Runner: r, GOOS: runtime.GOOS, Path: path, PathExt: ".EXE;.CMD;.PS1", Dir: dir}
}

func TestPATHOwnershipShadowingAndEquivalentLinks(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one")
	two := filepath.Join(dir, "two")
	three := filepath.Join(dir, "three")
	four := filepath.Join(dir, "four")
	command := nativeExecutable("rg")
	brew := filepath.Join(dir, "cellar", "ripgrep", "1", "bin", command)
	cargo := filepath.Join(two, command)
	writeFile(t, brew, "brew", 0755)
	writeFile(t, cargo, "cargo", 0755)
	linkFile(t, brew, filepath.Join(one, command))
	linkFile(t, brew, filepath.Join(three, command))
	linkFile(t, filepath.Join(dir, "gone"), filepath.Join(four, command))
	runner := &fakeRunner{}
	engine := nativeTestEngine(runner, nativePATH(one, two, three, one, four), dir)
	packages := []domain.Package{{Manager: "brew", ID: "ripgrep", Root: filepath.Dir(filepath.Dir(brew))}, {Manager: "cargo", ID: "ripgrep", ExecutablePaths: []string{cargo}}}
	report, err := engine.Diagnose(context.Background(), "rg", packages)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Executables) != 4 {
		t.Fatalf("repeated PATH directory should collapse: %#v", report.Executables)
	}
	first := findExecutable(t, report, filepath.Join(one, command))
	if !first.Preferred || first.Manager != "brew" {
		t.Fatalf("incorrect first candidate: %#v", first)
	}
	if e := findExecutable(t, report, filepath.Join(three, command)); e.EquivalentTo != first.Path {
		t.Fatalf("symlink counted twice: %#v", e)
	}
	if e := findExecutable(t, report, cargo); e.Manager != "cargo" || e.Preferred {
		t.Fatalf("incorrect cargo association: %#v", e)
	}
	counts := map[string]int{}
	for _, f := range report.Findings {
		counts[f.Kind]++
	}
	if counts["shadowed"] != 1 || counts["broken"] != 1 {
		t.Fatalf("unexpected findings: %#v", report.Findings)
	}
	if len(runner.calls) != 0 {
		t.Fatal("diagnose executed a discovered binary")
	}
}

func TestSpecificPackageBeatsRuntimeBinScan(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "mise", "installs", "node", "24")
	script := filepath.Join(root, "lib", "node_modules", "tool", "cli.js")
	path := filepath.Join(root, "bin", nativeExecutable("tool"))
	writeFile(t, script, "script", 0755)
	writeFile(t, path, "registered entrypoint", 0755)
	packages := []domain.Package{{Manager: "mise", ID: "node", Root: root, ExecutablePaths: []string{path}}, {Manager: "npm", ID: "tool", Root: filepath.Dir(script), ExecutablePaths: []string{path, script}}}
	e := nativeTestEngine(&fakeRunner{}, filepath.Dir(path), dir)
	r, err := e.Diagnose(context.Background(), "tool", packages)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 1 || r.Executables[0].Manager != "npm" {
		t.Fatalf("runtime swallowed exact package owner: %#v", r)
	}
}

func TestWholeScanDispatchersAreNotExtraInstallations(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "mise", "installs", "node", "24")
	path := filepath.Join(root, "bin", nativeExecutable("node"))
	shim := filepath.Join(dir, "mise", "shims", nativeExecutable("node"))
	writeFile(t, path, "node", 0755)
	writeFile(t, shim, "dispatcher", 0755)
	runner := &fakeRunner{}
	e := nativeTestEngine(runner, nativePATH(filepath.Dir(shim), filepath.Dir(path)), dir)
	r, err := e.Diagnose(context.Background(), "", []domain.Package{{Manager: "mise", ID: "node", Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Findings {
		if f.Kind == "shadowed" || f.Kind == "runtime-versions" {
			t.Fatalf("unresolved dispatcher counted as installation: %#v", r)
		}
	}
	if !findExecutable(t, r, shim).Preferred || findExecutable(t, r, path).Preferred || len(runner.calls) != 0 {
		t.Fatal("whole scan resolved shims or invented winner", r)
	}
}

func TestUnknownRuntimeAndNonExecutable(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	writeFile(t, filepath.Join(a, nativeExecutable("node")), "one", 0755)
	writeFile(t, filepath.Join(b, nativeExecutable("node")), "two", 0755)
	writeFile(t, filepath.Join(a, "note"), "not executable", 0644)
	e := nativeTestEngine(nil, nativePATH(a, b), dir)
	pkgs := []domain.Package{{Manager: "mise", ID: "node", Root: a}, {Manager: "mise", ID: "node", Root: b}}
	r, err := e.Diagnose(context.Background(), "", pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 2 || len(r.Findings) != 1 || r.Findings[0].Kind != "runtime-versions" {
		t.Fatalf("normal runtime versions: %#v", r)
	}
	r, err = e.Diagnose(context.Background(), "node", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Executables[0].Evidence[0].Kind != "unknown" {
		t.Fatal("unknown path was assigned an installer")
	}
}

func TestWindowsPATHAndPATHEXTOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "first")
	b := filepath.Join(dir, "second")
	writeFile(t, filepath.Join(a, "Tool.CMD"), "cmd", 0644)
	writeFile(t, filepath.Join(b, "tool.EXE"), "exe", 0644)
	writeFile(t, filepath.Join(b, "tool.CMD"), "cmd", 0644)
	writeFile(t, filepath.Join(a, "ignored.txt"), "text", 0644)
	writeFile(t, filepath.Join(a, "outside-pathext.ps1"), "script", 0755)
	if err := os.Mkdir(filepath.Join(a, "directory.EXE"), 0755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{GOOS: "windows", Path: a + ";" + b, PathExt: ".EXE;.CMD", Dir: dir}
	r, err := e.Diagnose(context.Background(), "TOOL", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 3 || r.Executables[0].Path != filepath.Join(a, "Tool.CMD") || !r.Executables[0].Preferred || r.Executables[1].Path != filepath.Join(b, "tool.EXE") {
		t.Fatalf("wrong directory/extension precedence: %#v", r.Executables)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("case insensitive names must share a group: %#v", r.Findings)
	}
	all, err := e.Diagnose(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Issues) != 0 || len(all.Executables) != 3 {
		t.Fatalf("Windows scan must preserve drive paths and select PATHEXT files only: %#v", all)
	}
}

func TestWindowsEmptyAndRelativePATHComponents(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "tool.CMD")
	second := filepath.Join(dir, "bin", "tool.EXE")
	writeFile(t, first, "cwd command", 0644)
	writeFile(t, second, "relative executable", 0644)
	e := &Engine{GOOS: "windows", Path: ";bin;", PathExt: ".EXE;.CMD", Dir: dir}
	r, err := e.Diagnose(context.Background(), "TOOL", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Issues) != 0 || len(r.Executables) != 2 {
		t.Fatalf("Windows relative PATH resolution: %#v", r)
	}
	if r.Executables[0].Path != first || !r.Executables[0].Preferred || r.Executables[0].PathIndex != 0 || r.Executables[1].Path != second || r.Executables[1].PathIndex != 1 {
		t.Fatalf("PATH directory order must precede PATHEXT order: %#v", r.Executables)
	}
}

func TestScoopShimIsEquivalentToTargetButArgumentsAreNot(t *testing.T) {
	dir := t.TempDir()
	shims := filepath.Join(dir, "shims")
	real := filepath.Join(dir, "apps", "tool", "1")
	target := filepath.Join(real, "tool.exe")
	writeFile(t, target, "binary", 0644)
	writeFile(t, filepath.Join(shims, "tool.exe"), "shim", 0644)
	writeFile(t, filepath.Join(shims, "tool.cmd"), "wrapper", 0644)
	writeFile(t, filepath.Join(shims, "tool.shim"), "path = \""+target+"\"\n", 0644)
	e := &Engine{GOOS: "windows", Path: shims + ";" + real, PathExt: ".EXE;.CMD", Dir: dir}
	r, err := e.Diagnose(context.Background(), "tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 3 || len(r.Findings) != 0 || r.Executables[1].EquivalentTo == "" || r.Executables[2].EquivalentTo == "" {
		t.Fatalf("same shim target reported as duplication: %#v", r)
	}
	writeFile(t, filepath.Join(shims, "tool.shim"), "path = \""+target+"\"\nargs = --special\n", 0644)
	r, err = e.Diagnose(context.Background(), "tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 {
		t.Fatal("shim with fixed arguments must retain distinct behavior")
	}
}

func TestCancellationAndNameValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := nativeTestEngine(nil, t.TempDir(), t.TempDir())
	if _, err := e.Diagnose(ctx, "rg", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := e.Diagnose(context.Background(), "../../bin/sh", nil); err == nil {
		t.Fatal("path accepted as command name")
	}
}

func TestEmptyAndRelativePATHComponentsUseDiagnosticDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, nativeExecutable("tool")), "cwd", 0755)
	writeFile(t, filepath.Join(dir, "bin", nativeExecutable("tool")), "relative", 0755)
	e := nativeTestEngine(nil, nativePATH("", "bin", ""), dir)
	r, err := e.Diagnose(context.Background(), "tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 2 || !r.Executables[0].Preferred || r.Executables[0].PathIndex != 0 || r.Executables[1].PathIndex != 1 {
		t.Fatalf("relative PATH resolution: %#v", r)
	}
}

func TestEnrichmentUsesRecordsAndPreservesPartialResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Homebrew keg and Unix npm symlink layout; Windows enrichment has separate fixtures")
	}
	dir := t.TempDir()
	cellar := filepath.Join(dir, "cellar")
	brewBinary := filepath.Join(cellar, "ripgrep", "1.0", "bin", "rg")
	writeFile(t, brewBinary, "bin", 0755)
	npmPrefix := filepath.Join(dir, "npm")
	npmRoot := filepath.Join(npmPrefix, "lib", "node_modules")
	npmPkg := filepath.Join(npmRoot, "@scope", "tool")
	script := filepath.Join(npmPkg, "cli.js")
	writeFile(t, filepath.Join(npmPkg, "package.json"), `{"name":"@scope/tool","bin":{"hello":"cli.js"}}`, 0644)
	writeFile(t, script, "node", 0755)
	linkFile(t, script, filepath.Join(npmPrefix, "bin", "hello"))
	uvRoot := filepath.Join(dir, "uv", "ruff")
	uvPath := filepath.Join(dir, "local", "bin", "ruff")
	writeFile(t, uvPath, "bin", 0755)
	cargoRoot := filepath.Join(dir, "cargo")
	t.Setenv("CARGO_INSTALL_ROOT", cargoRoot)
	miseRoot := filepath.Join(dir, "mise", "node", "22")
	writeFile(t, filepath.Join(miseRoot, "bin", "node"), "node", 0755)
	runner := &fakeRunner{output: map[string]string{
		commandKey("brew", "--cellar"):                         cellar,
		commandKey("brew", "info", "--json=v2", "--installed"): `{"formulae":[{"name":"ripgrep","full_name":"ripgrep","linked_keg":"1.0","installed":[{"version":"1.0"}]}]}`,
		commandKey("npm", "root", "--global"):                  npmRoot, commandKey("npm", "prefix", "--global"): npmPrefix,
		commandKey("uv", "--color", "never", "--no-progress", "tool", "list", "--show-paths"): "ruff v1.0 (" + uvRoot + ")\n- ruff (" + uvPath + ")\n",
		commandKey("cargo", "install", "--list", "--root", cargoRoot):                         "ripgrep v1.0:\n    rg\n",
	}}
	input := []domain.Package{{Manager: "brew", ID: "ripgrep", Version: "1.0"}, {Manager: "npm", ID: "@scope/tool"}, {Manager: "uvx", ID: "ruff"}, {Manager: "cargo", ID: "ripgrep"}, {Manager: "mise", ID: "node", Root: miseRoot}, {Manager: "winget", ID: "Some.App", Evidence: []domain.Evidence{{Kind: "recognized", Source: "winget"}}}}
	e := &Engine{Runner: runner, GOOS: "linux", Dir: dir}
	out, issues := e.Enrich(context.Background(), input)
	if len(issues) != 0 {
		t.Fatalf("unexpected enrichment errors: %#v", issues)
	}
	for i := 0; i < 5; i++ {
		if len(out[i].ExecutablePaths) == 0 || len(out[i].Evidence) == 0 {
			t.Fatalf("missing enrichment for %s: %#v", out[i].Manager, out[i])
		}
	}
	if strings.Join(out[1].Commands, ",") != "hello" {
		t.Fatalf("internal npm script leaked into public commands: %v", out[1].Commands)
	}
	if out[5].Evidence[0].Kind != "recognized" || len(input[0].Commands) != 0 {
		t.Fatal("modified source evidence or caller's records")
	}
	for _, call := range runner.calls {
		if call.Env["NO_COLOR"] != "1" {
			t.Fatal("queries should be reproducible")
		}
	}
	runner.failures = map[string]error{commandKey("brew", "--cellar"): errors.New("brew unavailable")}
	out, issues = e.Enrich(context.Background(), input)
	if len(issues) != 1 || issues[0].Manager != "brew" || len(out[1].ExecutablePaths) == 0 {
		t.Fatalf("a failed manager erased other results: %#v %#v", out, issues)
	}
}

func TestDpkgUsesExactOwnershipAndKeepsSuccessfulOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dpkg owns POSIX executable paths and is not a native Windows manager")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "tool[1]")
	writeFile(t, binary, "bin", 0755)
	target, _, _ := resolve(binary)
	args := []string{"-S", "--"}
	for _, p := range sortedUnique([]string{binary, target}) {
		args = append(args, escapeGlob(p))
	}
	key := commandKey("dpkg-query", args...)
	runner := &fakeRunner{output: map[string]string{key: "tool-package: " + binary + "\nwrong: /other\n"}, failures: map[string]error{key: errors.New("some other query path was not found")}}
	e := &Engine{Runner: runner, GOOS: "linux", Path: dir, Dir: dir}
	out, issues := e.Enrich(context.Background(), []domain.Package{{Manager: "apt", ID: "tool-package"}})
	if len(issues) != 0 || len(out[0].ExecutablePaths) != 1 || out[0].ExecutablePaths[0] != binary {
		t.Fatalf("exact owner not retained: %#v %#v", out, issues)
	}
}

func TestWindowsNPMWrappersAndScoopOwnership(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "npm")
	root := filepath.Join(prefix, "node_modules")
	pkgRoot := filepath.Join(root, "example")
	script := filepath.Join(pkgRoot, "cli.js")
	writeFile(t, filepath.Join(pkgRoot, "package.json"), `{"name":"example","bin":"cli.js"}`, 0644)
	writeFile(t, script, "script", 0644)
	cli := filepath.Join(root, "npm", "bin", "npm-cli.js")
	node := filepath.Join(prefix, "node.exe")
	writeFile(t, filepath.Join(prefix, "npm.cmd"), "launcher", 0644)
	writeFile(t, cli, "npm", 0644)
	writeFile(t, node, "node", 0644)
	for _, ext := range []string{".cmd", ".ps1"} {
		forward := "%*"
		if ext == ".ps1" {
			forward = "$args"
		}
		writeFile(t, filepath.Join(prefix, "example"+ext), "node \"%dp0%/node_modules/example/cli.js\" "+forward, 0644)
	}
	scoopPath := filepath.Join(dir, "shims", "jq.exe")
	writeFile(t, scoopPath, "shim", 0644)
	scoopJSON, _ := json.Marshal(map[string]any{"Name": "jq", "Path": scoopPath, "Source": "jq", "IsGlobal": false})
	runner := &fakeRunner{output: map[string]string{
		commandKey(node, cli, "root", "--global"): root, commandKey(node, cli, "prefix", "--global"): prefix,
		commandKey("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "& { scoop shim list | Select-Object Name,Path,Source,Type,IsGlobal | ConvertTo-Json -Compress }"): string(scoopJSON),
	}}
	e := &Engine{Runner: runner, GOOS: "windows", Path: prefix, PathExt: ".CMD;.PS1", Dir: dir}
	packages, issues := e.Enrich(context.Background(), []domain.Package{{Manager: "npm", ID: "example"}, {Manager: "scoop", ID: "jq"}})
	if len(issues) != 0 || len(packages[1].ExecutablePaths) != 1 {
		t.Fatalf("enrichment failure: %#v %#v", packages, issues)
	}
	r, err := e.Diagnose(context.Background(), "example", packages)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 2 || len(r.Findings) != 0 || r.Executables[1].EquivalentTo == "" {
		t.Fatalf("npm wrappers are one entrypoint: %#v", r)
	}
	writeFile(t, filepath.Join(prefix, "example.cmd"), "node \"%dp0%/node_modules/example/cli.js\" --special %*", 0644)
	r, err = e.Diagnose(context.Background(), "example", packages)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 {
		t.Fatal("wrapper with fixed arguments was treated as equivalent")
	}
}

func TestWindowsEnrichmentUsesRecordsAndPreservesPartialResults(t *testing.T) {
	dir := t.TempDir()
	uvRoot := filepath.Join(dir, "uv", "ruff")
	uvPath := filepath.Join(dir, "local", "bin", "ruff.exe")
	writeFile(t, uvPath, "binary", 0644)
	cargoRoot := filepath.Join(dir, "cargo")
	t.Setenv("CARGO_INSTALL_ROOT", cargoRoot)
	miseRoot := filepath.Join(dir, "mise", "installs", "node", "22")
	misePath := filepath.Join(miseRoot, "bin", "node.exe")
	writeFile(t, misePath, "node", 0644)
	uvKey := commandKey("uv", "--color", "never", "--no-progress", "tool", "list", "--show-paths")
	runner := &fakeRunner{output: map[string]string{
		uvKey: "ruff v1.0 (" + uvRoot + ")\n- ruff (" + uvPath + ")\n",
		commandKey("cargo", "install", "--list", "--root", cargoRoot): "ripgrep v1.0:\n    rg.exe\n",
	}}
	input := []domain.Package{
		{Manager: "uvx", ID: "ruff"},
		{Manager: "cargo", ID: "ripgrep"},
		{Manager: "mise", ID: "node", Root: miseRoot},
		{Manager: "winget", ID: "Some.App", Evidence: []domain.Evidence{{Kind: "recognized", Source: "winget"}}},
	}
	e := &Engine{Runner: runner, GOOS: "windows", Path: dir, PathExt: ".EXE;.CMD", Dir: dir}
	out, issues := e.Enrich(context.Background(), input)
	if len(issues) != 0 {
		t.Fatalf("unexpected enrichment errors: %#v", issues)
	}
	for i, path := range []string{uvPath, filepath.Join(cargoRoot, "bin", "rg.exe"), misePath} {
		if len(out[i].ExecutablePaths) != 1 || out[i].ExecutablePaths[0] != path || len(out[i].Evidence) == 0 {
			t.Fatalf("missing native Windows entrypoint for %s: %#v", out[i].Manager, out[i])
		}
	}
	if len(input[0].Commands) != 0 || out[3].Evidence[0].Kind != "recognized" {
		t.Fatal("enrichment changed caller records or strengthened WinGet provenance")
	}
	runner.failures = map[string]error{uvKey: errors.New("uv unavailable")}
	out, issues = e.Enrich(context.Background(), input)
	if len(issues) != 1 || issues[0].Manager != "uvx" || len(out[1].ExecutablePaths) != 1 || len(out[2].ExecutablePaths) != 1 {
		t.Fatalf("a failed provider erased independent Windows results: %#v %#v", out, issues)
	}
}

func TestKnownEntrypointsOutsidePATHAndMiseShimSelection(t *testing.T) {
	dir := t.TempDir()
	shimDir := filepath.Join(dir, "mise", "shims")
	activeRoot := filepath.Join(dir, "mise", "installs", "node", "22")
	inactiveRoot := filepath.Join(dir, "mise", "installs", "node", "20")
	active := filepath.Join(activeRoot, "bin", nativeExecutable("node"))
	inactive := filepath.Join(inactiveRoot, "bin", nativeExecutable("node"))
	shim := filepath.Join(shimDir, nativeExecutable("node"))
	writeFile(t, active, "active", 0755)
	writeFile(t, inactive, "inactive", 0755)
	writeFile(t, shim, "mise dispatcher", 0755)
	runner := &fakeRunner{output: map[string]string{commandKey("mise", "which", "node"): active}}
	e := nativeTestEngine(runner, shimDir, dir)
	pkgs := []domain.Package{{Manager: "mise", ID: "node", Version: "22", Root: activeRoot, Commands: []string{"node"}, ExecutablePaths: []string{active}, Active: true}, {Manager: "mise", ID: "node", Version: "20", Root: inactiveRoot, Commands: []string{"node"}, ExecutablePaths: []string{inactive}}}
	r, err := e.Diagnose(context.Background(), "node", pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 3 {
		t.Fatalf("missing installed entrypoints: %#v", r)
	}
	if first := r.Executables[0]; !first.Preferred || first.PackageKey != pkgs[0].Key() {
		t.Fatalf("mise selected runtime not attributed: %#v", first)
	}
	if x := findExecutable(t, r, active); x.PathIndex != -1 || x.Preferred || x.EquivalentTo != shim {
		t.Fatalf("active target duplicated: %#v", x)
	}
	if len(r.Findings) != 1 || r.Findings[0].Kind != "inactive-runtime" {
		t.Fatalf("inactive runtime labeled shadowed: %#v", r.Findings)
	}
	if len(runner.calls) != 1 || runner.calls[0].Env["MISE_AUTO_INSTALL"] != "0" || runner.calls[0].Env["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" {
		t.Fatal("shim query must be bounded and disable autoinstall")
	}
}

func TestOffPATHExecutableIsNeverPreferred(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "installed", nativeExecutable("tool"))
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, bin, "binary", 0755)
	e := nativeTestEngine(nil, empty, dir)
	r, err := e.Diagnose(context.Background(), "tool", []domain.Package{{Manager: "uvx", ID: "tool", Commands: []string{"tool"}, ExecutablePaths: []string{bin}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 1 || r.Executables[0].Preferred || r.Executables[0].PathIndex != -1 || len(r.Findings) != 1 || r.Findings[0].Kind != "not-on-path" {
		t.Fatalf("off PATH record was preferred: %#v", r)
	}
}

func TestMiseSharedDispatcherRootDoesNotClaimCargoTools(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "cargo", "bin")
	binary := filepath.Join(shared, nativeExecutable("rg"))
	writeFile(t, binary, "cargo binary", 0755)
	root := filepath.Join(dir, "mise", "installs", "rust", "stable")
	linkFile(t, shared, root)
	dotnet := filepath.Join(dir, "mise", "installs", "dotnet", "10")
	writeFile(t, filepath.Join(dotnet, nativeExecutable("dotnet")), "dotnet", 0755)
	e := nativeTestEngine(nil, shared, dir)
	pkgs, issues := e.Enrich(context.Background(), []domain.Package{{Manager: "mise", ID: "rust", Root: root}, {Manager: "mise", ID: "dotnet", Root: dotnet}})
	if len(issues) != 0 || len(pkgs[0].ExecutablePaths) != 0 || len(pkgs[1].ExecutablePaths) != 1 {
		t.Fatalf("incorrect runtime root scan: %#v %#v", pkgs, issues)
	}
	r, err := e.Diagnose(context.Background(), "rg", pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 1 || r.Executables[0].Manager != "" {
		t.Fatalf("shared Cargo directory attributed to mise/rust: %#v", r)
	}
}
