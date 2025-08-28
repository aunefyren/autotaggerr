package modules

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RunPicardTag resolves tags using MusicBrainz Picard for a known release MBID.
// Falls back to `beet import -A mbid:<id> <albumDir>` when Picard is not found.
// Env overrides:
//
//	PICARD_PATH=/full/path/to/picard
//	BEETS_PATH=/full/path/to/beet
func RunPicardTag(ctx context.Context, albumDir, releaseMBID string, logf func(format string, args ...any)) error {
	if albumDir == "" || releaseMBID == "" {
		return errors.New("albumDir and releaseMBID are required")
	}
	// Ensure absolute, normalized path
	absDir, err := filepath.Abs(albumDir)
	if err != nil {
		return fmt.Errorf("resolve albumDir: %w", err)
	}

	// 1) Try Picard
	// picardPath := findExec("picard", "PICARD_PATH");
	picardPath := "C:\\Program Files\\MusicBrainz Picard\\picard.exe"

	if picardPath != "" {
		if logf != nil {
			logf("Using Picard at %s", picardPath)
		}
		return runPicard(ctx, picardPath, absDir, releaseMBID, logf)
	}

	// 2) Fallback: beets
	if beetsPath := findExec("beet", "BEETS_PATH"); beetsPath != "" {
		if logf != nil {
			logf("Picard not found; falling back to beets at %s", beetsPath)
		}
		return runBeets(ctx, beetsPath, absDir, releaseMBID, logf)
	}

	return errors.New("neither Picard (picard) nor beets (beet) found in PATH; set PICARD_PATH/BEETS_PATH or install one of them")
}

// ---- helpers ----

func findExec(defaultName, envVar string) string {
	if p := os.Getenv(envVar); strings.TrimSpace(p) != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(defaultName); err == nil {
		return p
	}
	return ""
}

func picardSupportsLookupClustered(picardPath string) bool {
	out, _ := exec.Command(picardPath, "--version").Output()
	// crude check; improve with semver if you like
	return strings.Contains(string(out), "2.9") ||
		strings.Contains(string(out), "2.10") ||
		strings.Contains(string(out), "2.11") ||
		strings.Contains(string(out), "2.12") ||
		strings.Contains(string(out), "2.13")
}

func runPicard(ctx context.Context, picardPath, albumDir, releaseMBID string, logf func(string, ...any)) error {
	useLookupClustered := picardSupportsLookupClustered(picardPath)

	cmds := []string{
		fmt.Sprintf("LOAD mbid://release/%s", releaseMBID),
		fmt.Sprintf("LOAD %s", quotePicardPath(albumDir)),
		"CLUSTER",
	}
	if useLookupClustered {
		cmds = append(cmds, "LOOKUP_CLUSTERED")
	} else {
		cmds = append(cmds, "LOOKUP")
	}
	cmds = append(cmds,
		"SAVE_MATCHED",
		"REMOVE_SAVED",
		"REMOVE_EMPTY",
		"QUIT", // ensure app exits after processing
	)
	content := strings.Join(cmds, "\n") + "\n"

	tmp, err := os.CreateTemp("", "picard_cmds_*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	_ = tmp.Close()

	// Picard will still show a window; that’s normal. These flags keep it clean.
	args := []string{"-e", "FROM_FILE", tmp.Name(), "--no-crash-dialog", "--no-restore", "--no-player"}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, picardPath, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		if logf != nil {
			logf("Picard failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		return fmt.Errorf("picard run failed: %w", err)
	}
	return nil
}

// Beets fallback: beet import -A mbid:<ID> <albumDir>
func runBeets(ctx context.Context, beetPath, albumDir, releaseMBID string, logf func(format string, args ...any)) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
	}

	args := []string{
		"import",
		"-A", // use autotagger non-interactively
		"mbid:" + releaseMBID,
		albumDir,
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, beetPath, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if logf != nil {
		logf("Beets exec: %q %s", beetPath, strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		if logf != nil {
			logf("Beets failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		return fmt.Errorf("beets run failed: %w", err)
	}

	if logf != nil {
		out := strings.TrimSpace(stdout.String())
		if out != "" {
			logf("Beets stdout:\n%s", out)
		}
		errs := strings.TrimSpace(stderr.String())
		if errs != "" {
			logf("Beets stderr:\n%s", errs)
		}
	}
	return nil
}

// Quote path safely for Picard command file.
// Always use double quotes, escape embedded quotes.
func quotePicardPath(p string) string {
	q := strings.ReplaceAll(p, `"`, `\"`)
	// On Windows Picard accepts quoted backslash paths; on *nix, forward slashes are fine.
	if runtime.GOOS != "windows" {
		// Optional: normalize to forward slashes
		q = filepath.ToSlash(q)
	}
	return `"` + q + `"`
}
