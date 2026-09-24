// Package promptio transfers an already rendered prompt without recollecting it.
package promptio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func Copy(ctx context.Context, markdown string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var choices [][]string
	switch runtime.GOOS {
	case "darwin":
		choices = [][]string{{"pbcopy"}}
	case "windows":
		choices = [][]string{{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false); Set-Clipboard -Value ([Console]::In.ReadToEnd())"}}
	default:
		choices = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, args := range choices {
		path, err := exec.LookPath(args[0])
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, args[1:]...)
		cmd.Stdin = strings.NewReader(markdown)
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("clipboard failed: %w; the rendered prompt is retained", err)
		}
		return nil
	}
	return fmt.Errorf("no supported clipboard command is available; export or copy the preview manually")
}

// Export deliberately refuses overwriting an existing file, including symlinks.
func Export(path, markdown string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("export prompt: %w", err)
	}
	_, err = f.WriteString(markdown)
	closed := f.Close()
	if err == nil {
		err = closed
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("export prompt: %w", err)
	}
	return nil
}
