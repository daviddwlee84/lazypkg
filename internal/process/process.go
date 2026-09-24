package process

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type Result struct {
	Stdout string
	Stderr string
}
type Runner interface {
	Output(context.Context, domain.Command) (Result, error)
	Run(context.Context, domain.Command, io.Reader, io.Writer, io.Writer) error
}
type ExecRunner struct{ Env []string }

func Environment(base []string, c domain.Command) []string {
	if base == nil {
		base = os.Environ()
	}
	m := make(map[string]string)
	keyName := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	for _, s := range base {
		if k, v, ok := strings.Cut(s, "="); ok {
			m[keyName(k)] = v
		}
	}
	for _, k := range c.Unset {
		k = keyName(k)
		if strings.HasSuffix(k, "*") {
			for name := range m {
				if strings.HasPrefix(name, strings.TrimSuffix(k, "*")) {
					delete(m, name)
				}
			}
		} else {
			delete(m, k)
		}
	}
	for k, v := range c.Env {
		m[keyName(k)] = v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}
func (r ExecRunner) cmd(ctx context.Context, c domain.Command) *exec.Cmd {
	env := Environment(r.Env, c)
	path := c.Path
	lookupFailed := false
	if !strings.ContainsAny(path, "/\\") {
		lookupFailed = true
		for _, entry := range env {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || (key != "PATH" && !(runtime.GOOS == "windows" && strings.EqualFold(key, "PATH"))) {
				continue
			}
			for _, dir := range filepath.SplitList(value) {
				if !filepath.IsAbs(dir) {
					continue
				}
				if resolved, err := exec.LookPath(filepath.Join(dir, path)); err == nil {
					path = resolved
					lookupFailed = false
					break
				}
			}
			break
		}
	}
	cmd := exec.CommandContext(ctx, path, c.Args...)
	if lookupFailed {
		cmd.Err = fmt.Errorf("%s: executable not found in the configured PATH", c.Path)
	}
	cmd.Dir = c.Dir
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
func (r ExecRunner) Output(ctx context.Context, c domain.Command) (Result, error) {
	cmd := r.cmd(ctx, c)
	var out, errout limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	err := cmd.Run()
	res := Result{out.String(), errout.String()}
	if err != nil {
		return res, fmt.Errorf("%s: %w: %s", c.Path, err, strings.TrimSpace(res.Stderr))
	}
	if out.exceeded || errout.exceeded {
		return res, fmt.Errorf("%s output exceeds 32 MiB limit", c.Path)
	}
	return res, nil
}
func (r ExecRunner) Run(ctx context.Context, c domain.Command, in io.Reader, out, errout io.Writer) error {
	cmd := r.cmd(ctx, c)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errout
	return cmd.Run()
}

type limitedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 32*1024*1024 - b.Len()
	if n > remaining {
		b.exceeded = true
		p = p[:max(remaining, 0)]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func Display(c domain.Command) string {
	args := append([]string{c.Path}, c.Args...)
	for i, v := range args {
		if strings.ContainsAny(v, " \t\n\"';&|$`<>()") {
			args[i] = fmt.Sprintf("%q", v)
		}
	}
	return strings.Join(args, " ")
}
