package update

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Interval is how long a check's answer stands before GitHub is asked
// again. Every run of the program consults the answer; one run a day
// goes to the network for it.
const Interval = 24 * time.Hour

// retryAfter is how long a check that could not reach GitHub waits
// before the next run tries again. It is shorter than Interval so a
// release is not missed for a day over a moment offline, and longer than
// nothing so an afternoon offline does not cost every command a wait.
const retryAfter = time.Hour

// checkTimeout bounds the network call behind a check. A command that
// finishes before the check does waits for it this long at most.
const checkTimeout = 5 * time.Second

// CachePath is where the last check's answer is kept: latest-release.json
// in the user's cache directory, $XDG_CACHE_HOME/tasktracker on Linux.
// Empty when no cache directory is known, which turns the check off.
func CachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tasktracker", "latest-release.json")
}

// cached is the file at CachePath: the latest release the last check
// found, and when the next check is due.
type cached struct {
	Next   time.Time `json:"next"`
	Latest string    `json:"latest"`
}

// Check is a check for a newer release running in the background,
// started by Start so the program's own work overlaps the network call.
type Check struct {
	done   chan struct{}
	latest string
}

// Start begins a check for a release newer than current. Call Wait for
// the answer.
func Start(ctx context.Context, cachePath, current string) *Check {
	c := &Check{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		c.latest = Available(ctx, cachePath, current)
	}()
	return c
}

// Wait blocks until the check has finished, which checkTimeout bounds,
// and returns the newer release's version, or "" when there is nothing
// newer to tell of. It can be called from more than one place.
func (c *Check) Wait() string {
	<-c.done
	return c.latest
}

// Available reports the latest release's version when it is newer than
// current, and "" otherwise. The answer comes from the file at cachePath
// while the check recorded there is not yet due, and from GitHub
// otherwise, with the file rewritten; a dev build has nothing to compare
// with, so for one the answer is "" and nothing is read or fetched.
//
// Trouble reaching GitHub or reading the file is not reported, since the
// notice is a convenience that must not get in the way of the command
// that was run: the last answer stands, and the next try is brought
// forward to retryAfter. `tasktracker update --check` says what is wrong.
func Available(ctx context.Context, cachePath, current string) string {
	current = strings.TrimPrefix(current, "v")
	if cachePath == "" || !isRelease("v"+current) {
		return ""
	}
	c := readCache(cachePath)
	now := time.Now()
	// A next check further off than a whole Interval was written by a
	// clock that was ahead, and would otherwise keep GitHub from ever
	// being asked again; it is due now.
	if !now.Before(c.Next) || c.Next.After(now.Add(Interval)) {
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		rel, err := latest(ctx, &http.Client{})
		if err == nil {
			c = cached{Next: now.Add(Interval), Latest: strings.TrimPrefix(rel.TagName, "v")}
		} else {
			c.Next = now.Add(retryAfter)
		}
		writeCache(cachePath, c)
	}
	if c.Latest != "" && compare(current, c.Latest) == Older {
		return c.Latest
	}
	return ""
}

// readCache reads the file at path. A file that is missing or cannot be
// read as a cached answer is an empty one, so the next check is due now.
func readCache(path string) cached {
	var c cached
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return cached{}
	}
	return c
}

// writeCache writes c to path, making the directory when it is missing.
// A failure is not reported: the next run checks again, which is the
// worst that comes of it.
func writeCache(path string, c cached) {
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	// Written to a file of its own and renamed over, so a run that reads
	// the file at the same moment sees the old answer or the new one,
	// never half; and the CLI and the TUI can reach a due check together,
	// so each run writes a temporary file of its own rather than sharing
	// one name.
	tmp, err := os.CreateTemp(dir, "latest-release-*.json")
	if err != nil {
		return
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
}

// isRelease reports whether v, with its leading v, is a version a release
// can be compared with. A pseudo-version with no tag behind it
// (v0.0.0-<time>-<hash>, from a checkout that can see no tags) is a dev
// build, not an old release, so it is not. Build metadata such as +dirty
// is ignored, as semver says it should be.
func isRelease(v string) bool {
	if !semver.IsValid(v) {
		return false
	}
	if module.IsPseudoVersion(v) {
		// An empty base means no tag lies behind the version. An error
		// covers the same with build metadata attached, such as +dirty.
		if base, err := module.PseudoVersionBase(v); err != nil || base == "" {
			return false
		}
	}
	return true
}
