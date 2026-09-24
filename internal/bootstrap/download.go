package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

const releaseBase = "https://github.com/kdeldycke/meta-package-manager/releases/download/v" + domain.MPMVersion + "/"
const releaseMetadata = "https://api.github.com/repos/kdeldycke/meta-package-manager/releases/tags/v" + domain.MPMVersion

// Digests are pinned from the official v8.0.1 GitHub release metadata. The live
// metadata must agree before we offer a plan, and downloaded bytes must agree too.
var releaseDigests = map[string]string{
	"darwin/arm64":  "a03538b23e20b865d4de3f26bb9ca0ce1af2f6f652239c3c3dbd29a969234b83",
	"darwin/amd64":  "a75bbce996e6700008bad34b58168aba72c5d070f7b23427c42c6a5270be7071",
	"linux/arm64":   "485b3f37839135c73447b04b97557a4bed777b49a3f809f75b96c8b5140bc433",
	"linux/amd64":   "96d0fc65aba3c1d26af340862a70bc92b15aac43b454c3823b603e302393a194",
	"windows/arm64": "3d930495dec39cab686fc4eb5497c752ebe921962ec88f089c7898b7393156c9",
	"windows/amd64": "e54baba6122dbc376474fe298ea21afe0bbcdf94246cd046f16472baa497851b",
}

type asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

func (e *Engine) expectedAsset() (asset, error) {
	digest, ok := releaseDigests[e.GOOS+"/"+e.GOARCH]
	if !ok {
		return asset{}, fmt.Errorf("no verified mpm standalone release for %s/%s", e.GOOS, e.GOARCH)
	}
	platform := e.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	arch := e.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	ext := ".bin"
	if e.GOOS == "windows" {
		ext = ".exe"
	}
	name := "meta-package-manager-" + domain.MPMVersion + "-" + platform + "-" + arch + ext
	return asset{Name: name, URL: releaseBase + name, Digest: digest}, nil
}

func (e *Engine) releaseAsset(ctx context.Context) (asset, error) {
	if !filepath.IsAbs(e.DataDir) {
		return asset{}, errors.New("standalone setup requires an absolute lazypkg data directory")
	}
	want, err := e.expectedAsset()
	if err != nil {
		return asset{}, err
	}
	body, err := e.fetch(ctx, releaseMetadata, 2*1024*1024)
	if err != nil {
		return asset{}, fmt.Errorf("read official mpm release metadata: %w", err)
	}
	var release struct {
		Tag    string  `json:"tag_name"`
		Assets []asset `json:"assets"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return asset{}, fmt.Errorf("invalid release metadata: %w", err)
	}
	if release.Tag != "v"+domain.MPMVersion {
		return asset{}, errors.New("official release metadata has an unexpected version")
	}
	for _, a := range release.Assets {
		if a.Name == want.Name {
			if a.URL != want.URL || a.Digest != "sha256:"+want.Digest {
				return asset{}, errors.New("release asset URL or SHA-256 does not match the pinned official release")
			}
			return want, nil
		}
	}
	return asset{}, fmt.Errorf("official release is missing %s", want.Name)
}

func (e *Engine) response(ctx context.Context, url string) (*http.Response, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, errors.New("installer downloads require HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lazypkg-bootstrap")
	if url == releaseMetadata {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	client := e.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	if resp.Request != nil && resp.Request.URL.Scheme != "https" {
		resp.Body.Close()
		return nil, errors.New("download redirected away from HTTPS")
	}
	return resp, nil
}

func (e *Engine) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := e.response(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("download exceeds size limit")
	}
	return body, nil
}

func (e *Engine) download(ctx context.Context, url, digest, dir, suffix string, limit int64) (path string, err error) {
	resp, err := e.response(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(dir, "lazypkg-installer-*"+suffix)
	if err != nil {
		return "", err
	}
	path = f.Name()
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return path, err
	}
	if n > limit {
		return path, errors.New("download exceeds size limit")
	}
	if n == 0 {
		return path, errors.New("download was empty")
	}
	if err = ctx.Err(); err != nil {
		return path, err
	}
	if digest != "" && hex.EncodeToString(h.Sum(nil)) != digest {
		return path, errors.New("download SHA-256 verification failed; existing installation was preserved")
	}
	if err = f.Sync(); err != nil {
		return path, err
	}
	err = f.Close()
	return path, err
}
