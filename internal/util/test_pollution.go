//go:build !windows

package util

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// TestPollutionResult holds counts of cleaned items from test pollution cleanup.
type TestPollutionResult struct {
	RogueDolt     int      // Rogue dolt servers killed
	StaleDirs     int      // Stale test temp dirs removed
	StalePIDs     int      // Stale PID/lock files removed
	DeadWorktrees int      // Dead dog worktrees pruned
	Errors        []string // Non-fatal errors encountered
}

// String returns a one-line summary of the cleanup result.
func (r TestPollutionResult) String() string {
	if r.RogueDolt == 0 && r.StaleDirs == 0 && r.StalePIDs == 0 && r.DeadWorktrees == 0 {
		return "Test pollution cleanup: clean"
	}
	return fmt.Sprintf("Test pollution cleanup: rogue_dolt=%d stale_dirs=%d stale_pids=%d dead_worktrees=%d",
		r.RogueDolt, r.StaleDirs, r.StalePIDs, r.DeadWorktrees)
}

// CleanTestPollution runs all four test pollution cleanup steps and returns counts.
// Safety invariants (never violated):
//   - Never kills the workspace's own legitimate dolt server
//   - Never removes dirs where lsof shows active file handles
//   - Never removes PID files where the PID is still alive
//   - Never prunes worktrees for dogs with live tmux sessions
func CleanTestPollution(townRoot string) TestPollutionResult {
	var result TestPollutionResult

	// Step 1: Kill rogue dolt servers (imposters on the configured port).
	// Delegates to gt dolt kill-imposters via subprocess so we share the
	// data-dir comparison logic already implemented there.
	killedDolt, err := killDoltImposters(townRoot)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("kill-imposters: %v", err))
	}
	result.RogueDolt = killedDolt

	// Step 2: Clean stale test temp dirs.
	staleDirs, errs := cleanStaleTempDirs()
	result.StaleDirs = staleDirs
	result.Errors = append(result.Errors, errs...)

	// Step 3: Remove stale PID/lock files.
	stalePIDs, errs := cleanStalePIDFiles()
	result.StalePIDs = stalePIDs
	result.Errors = append(result.Errors, errs...)

	// Step 4: Prune dead dog worktrees.
	deadWorktrees, errs := pruneDeadDogWorktrees(townRoot)
	result.DeadWorktrees = deadWorktrees
	result.Errors = append(result.Errors, errs...)

	return result
}

// killDoltImposters calls `gt dolt kill-imposters` and returns the number killed.
// We invoke the gt subcommand rather than calling doltserver.KillImposters directly
// to avoid an import cycle (cmd → util → doltserver → cmd).
func killDoltImposters(townRoot string) (int, error) {
	cmd := exec.Command("gt", "dolt", "kill-imposters")
	cmd.Dir = townRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		// kill-imposters returns exit 0 when no imposters found; any non-zero
		// exit is a genuine error.
		outStr := strings.TrimSpace(string(out))
		if outStr != "" {
			return 0, fmt.Errorf("%s", outStr)
		}
		return 0, err
	}
	// Count "Killed imposter" lines in output
	killed := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Killed") || strings.Contains(line, "killed") {
			killed++
		}
	}
	return killed, nil
}

// tmpDir returns the system temporary directory.
func tmpDir() string {
	if d := os.Getenv("TMPDIR"); d != "" {
		return d
	}
	return "/tmp"
}

// isDirInUse returns true if any process has open file handles inside dir.
// Uses lsof +D which recursively checks the directory tree.
func isDirInUse(dir string) bool {
	cmd := exec.Command("lsof", "+D", dir)
	err := cmd.Run()
	return err == nil // lsof exits 0 when it found open files
}

// cleanStaleTempDirs removes stale beads test directories from TMPDIR.
// A directory is removed only when lsof confirms no process has it open.
func cleanStaleTempDirs() (int, []string) {
	var cleaned int
	var errs []string

	patterns := []string{
		filepath.Join(tmpDir(), "beads-test-dolt-*"),
		filepath.Join(tmpDir(), "beads-bd-tests-*"),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			errs = append(errs, fmt.Sprintf("glob %s: %v", pattern, err))
			continue
		}
		for _, dir := range matches {
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				continue
			}

			// Safety check: skip if any process has files open inside.
			if isDirInUse(dir) {
				continue
			}

			// Make writable to handle dirs created with restrictive perms.
			_ = filepath.Walk(dir, func(path string, fi os.FileInfo, _ error) error {
				if fi != nil {
					_ = os.Chmod(path, fi.Mode()|0200)
				}
				return nil
			})

			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, fmt.Sprintf("remove %s: %v", dir, err))
				continue
			}
			cleaned++
		}
	}

	return cleaned, errs
}

// isPIDAlive returns true if the given PID exists in the process table.
func isPIDAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// cleanStalePIDFiles removes /tmp PID files whose recorded process is dead.
func cleanStalePIDFiles() (int, []string) {
	var cleaned int
	var errs []string

	patterns := []string{
		"/tmp/dolt-test-server-*.pid",
		"/tmp/beads-test-dolt-*.pid",
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			errs = append(errs, fmt.Sprintf("glob %s: %v", pattern, err))
			continue
		}
		for _, pidFile := range matches {
			data, err := os.ReadFile(pidFile)
			if err != nil {
				continue
			}
			pidStr := strings.TrimSpace(string(data))
			if pidStr == "" {
				// Empty PID file — safe to remove.
				if err := os.Remove(pidFile); err == nil {
					cleaned++
				}
				continue
			}
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				// Unreadable PID — skip to avoid false positives.
				continue
			}
			if isPIDAlive(pid) {
				// Process still alive — leave the file alone.
				continue
			}
			// PID is dead — remove the stale file.
			if err := os.Remove(pidFile); err != nil {
				errs = append(errs, fmt.Sprintf("remove %s: %v", pidFile, err))
				continue
			}
			cleaned++
		}
	}

	return cleaned, errs
}

// hasTmuxSession returns true if a tmux session with the given name exists.
func hasTmuxSession(name string) bool {
	err := exec.Command("tmux", "has-session", "-t", name).Run()
	return err == nil
}

// pruneDeadDogWorktrees removes worktree directories for dogs whose tmux
// sessions no longer exist. Uses `git worktree remove --force` for safety.
func pruneDeadDogWorktrees(townRoot string) (int, []string) {
	var pruned int
	var errs []string

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return 0, []string{fmt.Sprintf("home dir: %v", err)}
	}

	dogsDir := filepath.Join(homeDir, "gt", "deacon", "dogs")
	entries, err := os.ReadDir(dogsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // No dogs directory — nothing to do.
		}
		return 0, []string{fmt.Sprintf("read dogs dir: %v", err)}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dogName := entry.Name()
		sessionName := "dog-" + dogName

		// Only prune if the dog's tmux session is dead.
		if hasTmuxSession(sessionName) {
			continue
		}

		// Session is dead — look for leftover worktree dirs inside.
		dogDir := filepath.Join(dogsDir, dogName)
		rigEntries, err := os.ReadDir(dogDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("read dog dir %s: %v", dogName, err))
			continue
		}

		for _, rigEntry := range rigEntries {
			if !rigEntry.IsDir() {
				continue
			}
			rigrepo := filepath.Join(dogDir, rigEntry.Name())

			// Must have a .git file/dir (worktree marker) to be a git worktree.
			if _, err := os.Stat(filepath.Join(rigrepo, ".git")); err != nil {
				continue
			}

			// Find the main repository's git dir.
			commonDirOut, err := exec.Command("git", "-C", rigrepo, "rev-parse", "--git-common-dir").Output()
			if err != nil {
				errs = append(errs, fmt.Sprintf("git common-dir for %s/%s: %v", dogName, rigEntry.Name(), err))
				continue
			}
			commonDir := strings.TrimSpace(string(commonDirOut))
			if commonDir == "" {
				continue
			}

			// Use `git worktree remove --force` to prune cleanly.
			removeOut, err := exec.Command("git", "worktree", "remove", "--force", rigrepo).
				CombinedOutput()
			if err != nil {
				errs = append(errs, fmt.Sprintf("worktree remove %s/%s: %v (%s)",
					dogName, rigEntry.Name(), err, strings.TrimSpace(string(removeOut))))
				continue
			}
			pruned++
		}
	}

	return pruned, errs
}
