package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

// PyPIExact performs a named-project lookup, never a claimed full-text search.
func PyPIExact(ctx context.Context, client *http.Client, base, query string) (domain.Snapshot, error) {
	s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now(), Issues: []domain.Issue{{Manager: "uvx", Message: "uv tools: exact PyPI project-name lookup only; CLI entrypoints are verified by uv during installation"}}}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(query) {
		return s, nil
	}
	if base == "" {
		base = "https://pypi.org"
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/pypi/"+url.PathEscape(query)+"/json", nil)
	if err != nil {
		return s, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return s, nil
	}
	if resp.StatusCode != 200 {
		return s, fmt.Errorf("PyPI lookup: HTTP %d", resp.StatusCode)
	}
	var data struct {
		Info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Summary string `json:"summary"`
		} `json:"info"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&data); err != nil {
		return s, err
	}
	if data.Info.Name == "" {
		return s, fmt.Errorf("PyPI response missing project name")
	}
	s.Packages = append(s.Packages, domain.Package{Manager: "uvx", ID: data.Info.Name, Name: data.Info.Name, Latest: data.Info.Version, Description: data.Info.Summary, Scope: "user tool"})
	return s, nil
}
