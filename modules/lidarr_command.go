package modules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

// Writing to Lidarr.
//
// Everything else in this package reads. That was not an accident of scope — a tool
// that enriches tags has no business reshaping the library manager's own data, and the
// read-only boundary is what made it safe to point Autotaggerr at somebody's Lidarr in
// the first place.
//
// The one write that earns its way across is *RefreshArtist*, and it is a narrow
// exception in a specific sense: it does not tell Lidarr what its data should be, it
// asks Lidarr to reconcile against its own metadata source. That is the same operation
// Lidarr's scheduled task performs unprompted, and the same one the Refresh button on
// an artist page performs. Autotaggerr supplies only the timing.
//
// The timing is the whole value. Lidarr's scheduled refresh is throttled per artist, so
// an artist whose catalog is otherwise stable can hold a dead album ID indefinitely —
// measured on a real instance, a 24-hour scheduled pass left two artists holding IDs
// that resolved nowhere. Autotaggerr is the thing that noticed, so Autotaggerr is the
// thing that can ask at the moment it matters.
//
// No other write belongs here. Deleting an album, in particular, does not: an album
// whose ID has gone dead is usually *re-keyed* by a refresh rather than removed, so
// deleting it would destroy the row that was about to be repaired.

// lidarrCommandPollInterval is how often a queued command is re-checked. Lidarr
// refreshes an artist in seconds to tens of seconds depending on how much its metadata
// service has to fetch, so this is short enough to not dominate the wait and long
// enough to be free. A var so tests need not wait it out.
var lidarrCommandPollInterval = 2 * time.Second

// lidarrCommandTimeout bounds one wait. A refresh that has not finished by then has not
// failed — Lidarr may simply be busy — but the caller must not block a job queue on it,
// so the wait ends and the outcome is reported as unknown rather than as success.
var lidarrCommandTimeout = 3 * time.Minute

// Lidarr command completion states, as its API reports them.
const (
	lidarrCommandCompleted = "completed"
	lidarrCommandFailed    = "failed"
	lidarrCommandAborted   = "aborted"
)

// postJSON sends a command and decodes the response, with getJSON's error handling:
// the same proxy-versus-Lidarr distinction applies, and is if anything more important
// here, because a login portal answering 200 to a write would otherwise read as the
// write having succeeded.
func (c *LidarrClient) postJSON(pathWithQuery string, body any, dst any) error {
	url := c.BaseURL + pathWithQuery

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("lidarr POST %s: could not encode body: %w", url, err)
	}

	sentCookie := c.Cookie != nil && *c.Cookie != ""

	do := func() error {
		// Built inside the closure: a retried request cannot reuse a consumed body.
		req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("lidarr POST %s: could not build request: %w", url, err)
		}
		req.Header.Set("X-Api-Key", c.APIKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		if sentCookie {
			req.Header.Set("Cookie", *c.Cookie)
		}

		logger.Log.Tracef("lidarr POST %s (api key: %t, cookie: %t)", url, c.APIKey != "", sentCookie)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("lidarr POST %s: request failed: %w", url, err)
		}
		defer resp.Body.Close()

		where := "lidarr POST " + url
		redirected := false
		if resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != url {
			where += " (redirected to " + resp.Request.URL.String() + ")"
			redirected = true
		}

		// Lidarr answers 201 for an accepted command, 200 for a status read.
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, lidarrBodySnippet))
			return fmt.Errorf("%s -> %d %s: %s%s", where, resp.StatusCode,
				http.StatusText(resp.StatusCode), strings.TrimSpace(string(b)),
				authHint(resp.StatusCode, redirected, sentCookie))
		}

		if dst == nil {
			return nil
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return fmt.Errorf("%s: could not read response: %w", where, err)
		}
		if err := json.Unmarshal(b, dst); err != nil {
			return fmt.Errorf("%s -> %d but the body is not JSON (content-type %q): %w; body starts: %q%s",
				where, resp.StatusCode, resp.Header.Get("Content-Type"), err,
				strings.TrimSpace(string(b[:min(len(b), lidarrBodySnippet)])),
				authHint(resp.StatusCode, redirected, sentCookie))
		}
		return nil
	}

	if c.RateLimit != nil {
		return c.RateLimit(do)
	}
	return do()
}

// RefreshArtist asks Lidarr to re-read one artist from its metadata source, returning
// the command ID to wait on.
//
// Scoped to a single artist deliberately. The bodyless form of this command refreshes
// the entire library, which is minutes to hours of metadata-service traffic for a
// question about one album.
func (c *LidarrClient) RefreshArtist(artistID int64) (int64, error) {
	var cmd models.LidarrCommand
	err := c.postJSON("/api/v1/command", map[string]any{
		"name":     "RefreshArtist",
		"artistId": artistID,
	}, &cmd)
	if err != nil {
		return 0, err
	}
	if cmd.ID == 0 {
		return 0, fmt.Errorf("lidarr accepted the refresh for artist %d but returned no command id", artistID)
	}
	return cmd.ID, nil
}

// CommandStatus reads one command's state.
func (c *LidarrClient) CommandStatus(commandID int64) (models.LidarrCommand, error) {
	var cmd models.LidarrCommand
	err := c.getJSON(fmt.Sprintf("/api/v1/command/%d", commandID), &cmd)
	return cmd, err
}

// WaitForCommand blocks until a command reaches a terminal state, the timeout elapses,
// or a status read fails.
//
// A timeout is reported as `finished=false` with a nil error rather than as a failure.
// The refresh is still running inside Lidarr and will very likely land; what the caller
// loses is the ability to act on the result *now*, and treating that as an error would
// mark a repair as failed when it was merely slower than the caller could wait.
func (c *LidarrClient) WaitForCommand(commandID int64) (finished bool, err error) {
	deadline := time.Now().Add(lidarrCommandTimeout)
	for {
		cmd, err := c.CommandStatus(commandID)
		if err != nil {
			return false, err
		}
		switch cmd.Status {
		case lidarrCommandCompleted:
			return true, nil
		case lidarrCommandFailed, lidarrCommandAborted:
			return true, fmt.Errorf("lidarr command %d (%s) ended as %q: %s",
				commandID, cmd.Name, cmd.Status, cmd.Message)
		}
		if time.Now().After(deadline) {
			logger.Log.Warnf("lidarr command %d (%s) still %q after %s; not waiting further",
				commandID, cmd.Name, cmd.Status, lidarrCommandTimeout)
			return false, nil
		}
		time.Sleep(lidarrCommandPollInterval)
	}
}
