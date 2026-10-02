// Package selfupdate finds, verifies, and installs inari releases from
// GitHub, replacing the running executable.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hizkifw/inari/internal/buildinfo"
)

// The release source is fixed at build time. An override would let whoever
// controls the environment choose the binary inari installs over itself.
const (
	releasesURL = "https://github.com/hizkifw/inari/releases"
	latestAPI   = "https://api.github.com/repos/hizkifw/inari/releases/latest"
)

const (
	// A release archive is a few megabytes; this only stops a runaway
	// download.
	maxArchiveSize   = 32 << 20
	maxBinarySize    = 128 << 20
	maxChecksumsSize = 64 << 10
	maxAPIResponse   = 1 << 20
	verifyTimeout    = 10 * time.Second
)

// Updater talks to one fixed release source. Its fields are unexported so
// only this package's tests can point it elsewhere.
type Updater struct {
	releases string
	api      string
	// lookup does not follow redirects: the /releases/latest redirect target
	// names the tag without spending GitHub API quota.
	lookup   *http.Client
	download *http.Client
	goos     string
	goarch   string
}

func New() *Updater {
	return &Updater{
		releases: releasesURL,
		api:      latestAPI,
		lookup: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		download: &http.Client{Timeout: 5 * time.Minute},
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
	}
}

// Latest returns the newest published release. It reads the /releases/latest
// redirect first, as scripts/install.sh does, and falls back to the API for
// the few minutes after a release when GitHub serves the generic page instead.
func (u *Updater) Latest(ctx context.Context) (Version, error) {
	v, redirectErr := u.latestFromRedirect(ctx)
	if redirectErr == nil {
		return v, nil
	}
	v, apiErr := u.latestFromAPI(ctx)
	if apiErr != nil {
		return Version{}, fmt.Errorf("find latest release: %w", errors.Join(redirectErr, apiErr))
	}
	return v, nil
}

func (u *Updater) latestFromRedirect(ctx context.Context) (Version, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.releases+"/latest", nil)
	if err != nil {
		return Version{}, err
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	res, err := u.lookup.Do(req)
	if err != nil {
		return Version{}, err
	}
	res.Body.Close()
	if res.StatusCode < 300 || res.StatusCode >= 400 {
		return Version{}, fmt.Errorf("latest release redirect: HTTP %d", res.StatusCode)
	}
	location, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		return Version{}, fmt.Errorf("latest release redirect: %w", err)
	}
	return ParseVersion(path.Base(location.Path))
}

func (u *Updater) latestFromAPI(ctx context.Context) (Version, error) {
	body, err := u.fetch(ctx, u.api, maxAPIResponse)
	if err != nil {
		return Version{}, err
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return Version{}, fmt.Errorf("decode latest release: %w", err)
	}
	return ParseVersion(release.TagName)
}

// Install downloads release v for this platform, checks it against the
// release checksums, confirms that it runs and reports v, and then replaces
// the executable at target. On any error target is left untouched.
func (u *Updater) Install(ctx context.Context, v Version, target string) error {
	removeStale(target)
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	binary, err := u.fetchBinary(ctx, v)
	if err != nil {
		return err
	}
	candidate, err := writeCandidate(filepath.Dir(target), binary, info.Mode().Perm())
	if err != nil {
		return err
	}
	// After a successful replace the candidate no longer exists and this is a
	// no-op; on failure it removes the partial download.
	defer os.Remove(candidate)
	if err := verify(ctx, candidate, v); err != nil {
		return err
	}
	if err := replace(target, candidate); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// fetchBinary returns the executable from the platform archive after
// verifying the archive's checksum. Asset names follow scripts/release.sh;
// every earlier inari relies on them, so they must not change.
func (u *Updater) fetchBinary(ctx context.Context, v Version) ([]byte, error) {
	name := fmt.Sprintf("inari_%s_%s_%s", strings.TrimPrefix(v.String(), "v"), u.goos, u.goarch)
	archiveName, entry := name+".tar.gz", name+"/inari"
	if u.goos == "windows" {
		archiveName, entry = name+".zip", name+"/inari.exe"
	}
	base := u.releases + "/download/" + v.String() + "/"
	sums, err := u.fetch(ctx, base+"checksums.txt", maxChecksumsSize)
	if err != nil {
		return nil, err
	}
	want, err := checksum(sums, archiveName)
	if err != nil {
		return nil, err
	}
	archive, err := u.fetch(ctx, base+archiveName, maxArchiveSize)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s", archiveName)
	}
	if u.goos == "windows" {
		return extractZip(archive, entry)
	}
	return extractTarGz(archive, entry)
}

func (u *Updater) fetch(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// GitHub's API asks clients to identify themselves in the User-Agent.
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	res, err := u.download.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", rawURL, res.StatusCode)
	}
	body, err := readLimited(res.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}
	return body, nil
}

// checksum finds name in sha256sum output. checksums.sh writes paths as
// "./name", so a leading "./" is ignored.
func checksum(sums []byte, name string) (string, error) {
	for line := range strings.Lines(string(sums)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "./") == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", name)
}

// extractTarGz reads exactly one regular file. Every other entry is ignored,
// so a crafted path in the archive can never be written anywhere.
func extractTarGz(archive []byte, entry string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("release archive has no %s", entry)
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && strings.TrimPrefix(hdr.Name, "./") == entry {
			return readLimited(tr, maxBinarySize)
		}
	}
}

func extractZip(archive []byte, entry string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("read release archive: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != entry || !f.Mode().IsRegular() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return readLimited(rc, maxBinarySize)
	}
	return nil, fmt.Errorf("release archive has no %s", entry)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("exceeds size limit")
	}
	return b, nil
}

// writeCandidate stages the new binary beside the target so the final rename
// stays on one filesystem and is atomic.
func writeCandidate(dir string, binary []byte, perm os.FileMode) (string, error) {
	pattern := ".inari-upgrade-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	name := f.Name()
	_, err = f.Write(binary)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Chmod(name, perm)
	}
	if err != nil {
		return "", errors.Join(fmt.Errorf("write %s: %w", name, err), os.Remove(name))
	}
	return name, nil
}

// verify runs the candidate before it replaces anything. A binary for the
// wrong platform, a truncated file, or a mislabeled release fails here.
// "--version" exits before reading any config, and its "inari <tag>" output
// is part of the upgrade contract with earlier releases.
func verify(ctx context.Context, candidate string, v Version) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, candidate, "--version").Output()
	if err != nil {
		return fmt.Errorf("downloaded inari %s does not run: %w", v, err)
	}
	if got := strings.TrimSpace(string(out)); got != "inari "+v.String() {
		return fmt.Errorf("downloaded binary reports %q, want %q", got, "inari "+v.String())
	}
	return nil
}
