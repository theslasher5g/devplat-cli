package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultBaseURL is the release host. Overridable for testing only — a caller
// pointing this somewhere else still cannot install anything the compiled-in
// key did not sign.
const DefaultBaseURL = "https://get.devplat.ch"

// Downloader fetches and verifies a published release.
type Downloader struct {
	BaseURL string
	Client  *http.Client
}

func NewDownloader(baseURL string) *Downloader {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Downloader{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		// Generous but bounded: a slow connection should finish a ~10 MB
		// download, a hung one should not wedge `devplat upgrade` forever.
		Client: &http.Client{Timeout: 5 * time.Minute},
	}
}

func (d *Downloader) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := d.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned status %d", url, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

// ArchiveName is the published filename for a platform.
func ArchiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("devplat-%s-%s-%s.%s", version, goos, goarch, ext)
}

// FetchVerified downloads a release archive and returns its bytes only if the
// signed manifest vouches for them.
//
// The order is the security property, not an implementation detail:
//
//  1. fetch checksums.txt
//  2. fetch checksums.txt.sig and verify it against the compiled-in key
//  3. only then read the expected hash out of the manifest
//  4. fetch the archive and hash it
//
// Checking the archive against a manifest nobody vouched for would be checking
// the release host's claim against its own other claim. Steps 2 and 3 in that
// order are what make step 4 mean anything.
//
// A missing signature is a hard failure whenever a key is configured. If it
// were merely skipped, an attacker holding the release host would delete the
// .sig file and get the old, unprotected behaviour back for free.
func (d *Downloader) FetchVerified(ctx context.Context, version string) ([]byte, string, error) {
	name := ArchiveName(version, runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("%s/%s", d.BaseURL, version)

	manifest, err := d.get(ctx, base+"/checksums.txt", 1<<20)
	if err != nil {
		return nil, "", err
	}

	if IsConfigured() {
		sig, err := d.get(ctx, base+"/checksums.txt.sig", 1<<10)
		if err != nil {
			return nil, "", fmt.Errorf(
				"%s is not signed, and this build requires a signature: %w", version, errors.Join(ErrUnsigned, err))
		}
		if err := VerifyManifest(manifest, sig); err != nil {
			return nil, "", fmt.Errorf(
				"refusing to install %s: %w\nThis is what a tampered release host looks like — please report it to security@devplat.ch",
				version, err)
		}
	}

	want, err := ChecksumFor(manifest, name)
	if err != nil {
		return nil, "", err
	}

	// 64 MiB ceiling: an order of magnitude above any real archive, and the
	// difference between a corrupt response and an unbounded allocation.
	archive, err := d.get(ctx, base+"/"+name, 64<<20)
	if err != nil {
		return nil, "", err
	}
	if err := VerifyStream(strings.NewReader(string(archive)), want); err != nil {
		return nil, "", err
	}
	return archive, name, nil
}

// ExtractBinary pulls the devplat executable out of a .tar.gz release archive.
//
// Only the one expected filename is extracted, and never through a path the
// archive supplies. An archive is attacker-controlled input in the case that
// matters, and "../../etc/cron.d/x" written by a tar extractor running under
// sudo is the classic way a supply-chain problem becomes a root problem.
func ExtractBinary(archive []byte, into string) (string, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return "", fmt.Errorf("release archive is not valid gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("release archive is not a valid tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "devplat" {
			continue
		}
		// Destination is built from our own constant, never from hdr.Name.
		out := filepath.Join(into, "devplat")
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 256<<20)); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		return out, nil
	}
	return "", errors.New("the release archive contains no devplat binary")
}
