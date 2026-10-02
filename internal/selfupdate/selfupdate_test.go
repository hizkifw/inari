package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hizkifw/inari/internal/buildinfo"
)

func TestParseVersion(t *testing.T) {
	for _, s := range []string{"v0.1.0", "v1.20.3", "v10.0.0"} {
		v, err := ParseVersion(s)
		if err != nil || v.String() != s {
			t.Fatalf("ParseVersion(%q) = %v, %v; want a round trip", s, v, err)
		}
	}
	for _, s := range []string{"", "dev", "0.1.0", "v1.2", "v1.2.3.4", "v1.2.3-rc1", "v01.2.3", "v+1.2.3", "v1.-2.3", "v1.2.x", "../v1.2.3"} {
		if _, err := ParseVersion(s); err == nil {
			t.Fatalf("ParseVersion(%q) succeeded, want an error", s)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	order := []string{"v0.1.9", "v0.1.10", "v0.2.0", "v1.0.0"}
	for i := range order {
		for j := range order {
			a, _ := ParseVersion(order[i])
			b, _ := ParseVersion(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Fatalf("%s.Compare(%s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

// release is a fake GitHub release served by httptest.
type release struct {
	latest   http.HandlerFunc // /releases/latest
	api      http.HandlerFunc // the API's latest-release endpoint
	archive  []byte
	checksum string // checksums.txt body; computed from archive when empty
}

func (r *release) serve(t *testing.T) *Updater {
	t.Helper()
	mux := http.NewServeMux()
	if r.latest != nil {
		mux.HandleFunc("/releases/latest", r.latest)
	}
	if r.api != nil {
		mux.HandleFunc("/api/latest", r.api)
	}
	name := archiveName("v0.2.0")
	mux.HandleFunc("/releases/download/v0.2.0/"+name, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(r.archive)
	})
	mux.HandleFunc("/releases/download/v0.2.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		sums := r.checksum
		if sums == "" {
			sum := sha256.Sum256(r.archive)
			sums = fmt.Sprintf("%s  ./other.tar.gz\n%s  ./%s\n", strings.Repeat("0", 64), hex.EncodeToString(sum[:]), name)
		}
		fmt.Fprint(w, sums)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("User-Agent"); got != buildinfo.UserAgent() {
			t.Errorf("%s User-Agent = %q, want %q", req.URL.Path, got, buildinfo.UserAgent())
		}
		mux.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	u := New()
	u.releases, u.api = srv.URL+"/releases", srv.URL+"/api/latest"
	u.lookup.Transport, u.download.Transport = srv.Client().Transport, srv.Client().Transport
	return u
}

func archiveName(tag string) string {
	name := fmt.Sprintf("inari_%s_%s_%s", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

// tarGz builds a release archive from name → content pairs.
func tarGz(t *testing.T, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for i := 0; i < len(files); i += 2 {
		body := []byte(files[i+1])
		if err := tw.WriteHeader(&tar.Header{Name: files[i], Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

func redirectTo(location string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, location, http.StatusFound)
	}
}

func TestLatestReadsRedirect(t *testing.T) {
	u := (&release{
		latest: redirectTo("/hizkifw/inari/releases/tag/v0.2.0"),
		api:    func(w http.ResponseWriter, _ *http.Request) { t.Error("API used despite a valid redirect") },
	}).serve(t)
	v, err := u.Latest(context.Background())
	if err != nil || v.String() != "v0.2.0" {
		t.Fatalf("Latest = %v, %v; want v0.2.0", v, err)
	}
}

func TestLatestFallsBackToAPI(t *testing.T) {
	u := (&release{
		// GitHub briefly serves the release list instead of redirecting.
		latest: func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>") },
		api:    func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"tag_name":"v0.3.1"}`) },
	}).serve(t)
	v, err := u.Latest(context.Background())
	if err != nil || v.String() != "v0.3.1" {
		t.Fatalf("Latest = %v, %v; want v0.3.1", v, err)
	}
}

func TestLatestRejectsMalformedTags(t *testing.T) {
	u := (&release{
		latest: redirectTo("/hizkifw/inari/releases/tag/v0.2.0-evil"),
		api:    func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"tag_name":"../../x"}`) },
	}).serve(t)
	if v, err := u.Latest(context.Background()); err == nil {
		t.Fatalf("Latest = %v, want an error", v)
	}
}

// installTarget creates an executable standing in for the running inari.
func installTarget(t *testing.T) (dir, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install tests run a shell script as the candidate binary")
	}
	dir = t.TempDir()
	target = filepath.Join(dir, "inari")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, target
}

func script(output string) string { return "#!/bin/sh\necho '" + output + "'\n" }

func entryName() string {
	return strings.TrimSuffix(archiveName("v0.2.0"), ".tar.gz") + "/inari"
}

func TestInstallReplacesTarget(t *testing.T) {
	dir, target := installTarget(t)
	want := script("inari v0.2.0")
	u := (&release{archive: tarGz(t,
		"../inari", "escape",
		"inari_0.2.0_other/inari", "wrong",
		entryName(), want,
	)}).serve(t)
	if err := u.Install(context.Background(), mustVersion(t, "v0.2.0"), target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != want {
		t.Fatalf("target = %q, %v; want the release binary", got, err)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %v, want 0755", info.Mode().Perm())
	}
	assertOnly(t, dir, "inari")
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "inari")); err == nil {
		t.Fatal("a traversal entry was written outside the install directory")
	}
}

func TestInstallLeavesTargetOnFailure(t *testing.T) {
	sum := sha256.Sum256([]byte("different"))
	// Random bytes do not compress, so this otherwise valid release archive
	// still exceeds the download limit after gzip.
	padding := make([]byte, maxArchiveSize)
	rand.NewChaCha8([32]byte{}).Read(padding)
	for name, c := range map[string]struct {
		release *release
		want    string // the cause the error must name
	}{
		"checksum mismatch": {&release{
			archive:  tarGz(t, entryName(), script("inari v0.2.0")),
			checksum: hex.EncodeToString(sum[:]) + "  ./" + archiveName("v0.2.0") + "\n",
		}, "checksum mismatch"},
		"no checksum entry": {&release{
			archive:  tarGz(t, entryName(), script("inari v0.2.0")),
			checksum: strings.Repeat("0", 64) + "  ./other.tar.gz\n",
		}, "no checksum for"},
		"missing binary":    {&release{archive: tarGz(t, "README.md", "hi")}, "release archive has no"},
		"wrong version":     {&release{archive: tarGz(t, entryName(), script("inari v0.1.0"))}, `reports "inari v0.1.0"`},
		"binary won't run":  {&release{archive: tarGz(t, entryName(), "#!/bin/sh\nexit 3\n")}, "does not run"},
		"corrupt archive":   {&release{archive: []byte("not a gzip archive")}, "gzip: invalid header"},
		"oversized archive": {&release{archive: tarGz(t, entryName(), script("inari v0.2.0"), "padding", string(padding))}, "exceeds size limit"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, target := installTarget(t)
			u := c.release.serve(t)
			err := u.Install(context.Background(), mustVersion(t, "v0.2.0"), target)
			if err == nil {
				t.Fatal("Install succeeded, want an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Install error = %v, want it to name %q", err, c.want)
			}
			if got, _ := os.ReadFile(target); string(got) != "old" {
				t.Fatalf("target = %q, want it untouched", got)
			}
			assertOnly(t, dir, "inari")
		})
	}
}

func TestInstallReportsUnwritableDirectory(t *testing.T) {
	dir, target := installTarget(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	u := (&release{archive: tarGz(t, entryName(), script("inari v0.2.0"))}).serve(t)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := u.Install(context.Background(), mustVersion(t, "v0.2.0"), target)
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("Install error = %v, want it to name %s", err, dir)
	}
}

func TestExtractZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"../inari.exe": "escape", "inari_0.2.0_windows_amd64/inari.exe": "binary"} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	got, err := extractZip(buf.Bytes(), "inari_0.2.0_windows_amd64/inari.exe")
	if err != nil || string(got) != "binary" {
		t.Fatalf("extractZip = %q, %v; want the named entry", got, err)
	}
	if _, err := extractZip(buf.Bytes(), "inari_0.2.0_windows_arm64/inari.exe"); err == nil {
		t.Fatal("extractZip found an entry that does not exist")
	}
}

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// assertOnly fails when dir holds anything besides names, such as a staged
// candidate left behind by an error path.
func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("%s contains %v, want %v", dir, got, names)
	}
}
