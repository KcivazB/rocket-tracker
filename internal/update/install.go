package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Install downloads r's asset over exe and keeps the replaced file as
// exe+".old" (Windows lets a running executable be renamed, not overwritten).
// verify checks the downloaded file before the swap (e.g. runs it with
// "version"); it may be nil.
func Install(ctx context.Context, client *http.Client, r Release, exe string, verify func(path string) error) error {
	if r.Download == "" {
		return errors.New("the release has no download for this program")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	tmp := exe + ".new"
	if err := download(ctx, client, r.Download, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if verify != nil {
		if err := verify(tmp); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("downloaded file rejected: %w", err)
		}
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe) // put the running version back
		os.Remove(tmp)
		return err
	}
	return nil
}

// CleanupOld removes the executable an update replaced (it could not be
// deleted while it was running).
func CleanupOld(exe string) { os.Remove(exe + ".old") }

func download(ctx context.Context, client *http.Client, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return fmt.Errorf("download %s: got %d of %d bytes", url, n, resp.ContentLength)
	}
	return nil
}
