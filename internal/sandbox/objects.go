package sandbox

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The object-store half of the design.
//
// New objects are written to the pane's own store, with the repository's own
// store as a READ-ONLY alternate. That is what keeps `rm -rf` inside a sandbox
// from destroying the repository's entire local history — objects are every
// commit, tree and blob for every branch, and a writable store makes that a
// one-command, unrecoverable loss.
//
// The cost is that the host cannot see the new objects until it is told where
// to look, so Quil writes one line into <common>/objects/info/alternates.
// Measured: before the line, the host answers "fatal: bad object HEAD" for
// that worktree and `git fsck` reports an invalid sha1 pointer; after it,
// status, log and fsck are all correct.
//
// That line is also the one thing here that can break a user's repository, so
// two rules are absolute:
//
//   - Harvest BEFORE removing the line, and remove the line BEFORE deleting
//     the store. A stale line makes every git command in the repository fail.
//   - Remove only THIS pane's line. Two sandbox panes on one repository write
//     two lines, and the user may have written one of their own.

// alternatesRel is the file's location under a repository's object store.
const alternatesRel = "info/alternates"

// AlternatesPath is the file Quil adds a line to, for a given mapping.
func AlternatesPath(m Mapping) string {
	return joinHost(m.HostGitCommon, "objects", "info", "alternates")
}

// AlternatesLine is the line naming this pane's object store.
//
// The path is in the host's NATIVE form. Measured: an MSYS-style "/c/..."
// path makes git answer "error: unable to normalize alternate object path" on
// every command in the repository, which looks exactly like the corruption
// this line exists to prevent.
func AlternatesLine(m Mapping) string { return filepath.Clean(m.HostObjects()) }

// AddAlternate appends this pane's line to the repository's alternates file,
// creating it when absent. Idempotent: a line already present is left alone,
// so a restart cannot accumulate duplicates.
func AddAlternate(m Mapping) error {
	path := AlternatesPath(m)
	want := AlternatesLine(m)

	lines, err := readAlternates(path)
	if err != nil {
		return err
	}
	for _, ln := range lines {
		if sameHostPath(ln, want) {
			return nil
		}
	}
	return writeAlternates(path, append(lines, want))
}

// RemoveAlternate deletes only this pane's line.
//
// Rewriting the file wholesale would drop another sandbox pane's line — and
// its repository along with it — or a line the user put there themselves.
func RemoveAlternate(m Mapping) error {
	path := AlternatesPath(m)
	want := AlternatesLine(m)

	lines, err := readAlternates(path)
	if err != nil {
		return err
	}
	kept := lines[:0]
	found := false
	for _, ln := range lines {
		if sameHostPath(ln, want) {
			found = true
			continue
		}
		kept = append(kept, ln)
	}
	if !found {
		return nil
	}
	if len(kept) == 0 {
		// An empty alternates file is legal but pointless, and leaving one
		// behind in a repository Quil found without one is a visible trace
		// of a feature that is supposed to leave none.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("sandbox: remove alternates: %w", err)
		}
		return nil
	}
	return writeAlternates(path, kept)
}

// StaleAlternates returns the lines in path that point inside sandboxRoot and
// no longer resolve.
//
// The daemon calls this at startup. A line whose target is gone makes EVERY
// git command in that repository print an error, and the way that happens in
// practice is not a crash but a wipe: reset-daemon removes $QUIL_HOME while
// the lines still exist. Best-effort by nature — the record of which
// repositories to check lives in $QUIL_HOME too, so a wipe defeats it and the
// docs carry the one-line manual fix.
func StaleAlternates(path, sandboxRoot string) ([]string, error) {
	lines, err := readAlternates(path)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, ln := range lines {
		if !pathWithin(sandboxRoot, ln) {
			continue
		}
		if _, err := os.Stat(filepath.FromSlash(ln)); os.IsNotExist(err) {
			stale = append(stale, ln)
		}
	}
	return stale, nil
}

// PruneAlternates removes every stale sandbox line from path.
func PruneAlternates(path, sandboxRoot string) (removed int, err error) {
	stale, err := StaleAlternates(path, sandboxRoot)
	if err != nil || len(stale) == 0 {
		return 0, err
	}
	lines, err := readAlternates(path)
	if err != nil {
		return 0, err
	}
	drop := make(map[string]bool, len(stale))
	for _, s := range stale {
		drop[normalizeCompare(s)] = true
	}
	kept := lines[:0]
	for _, ln := range lines {
		if drop[normalizeCompare(ln)] {
			continue
		}
		kept = append(kept, ln)
	}
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		return len(stale), nil
	}
	return len(stale), writeAlternates(path, kept)
}

func readAlternates(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sandbox: read alternates: %w", err)
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		// A leading "#" is a comment in this file, and a blank line is
		// nothing. Both are preserved by being skipped only from the
		// MATCHING logic — but git treats neither as a path, so carrying
		// them through the rewrite unchanged is what preserving them means.
		if t := strings.TrimRight(ln, "\r"); strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// writeAlternates replaces the file atomically. It creates objects/info when
// absent: a repository that has never had an alternate has no such directory,
// and this is the one caller that can be the first to need it.
func writeAlternates(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sandbox: create objects/info: %w", err)
	}
	body := strings.Join(lines, "\n") + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("sandbox: write alternates: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("sandbox: rename alternates: %w", err)
	}
	return nil
}

// HarvestObjects copies the pane's new git objects into the repository's own
// object store, so they survive the pane.
//
// It copies ONLY the sharded loose-object directories and pack files, and
// NEVER info/. That scoping is not tidiness. The agent controls its store's
// info/ directory, git reads info/commit-graph as trusted ancestry and
// info/alternates transitively, and a "plain copy of the store" was measured
// moving an agent-written commit-graph and info/packs into the user's real
// object store after a container-side `git repack -a -d`.
//
// The name filter also makes the copy safe against a live container: git
// writes objects and packs atomically (temp file + rename), so a well-formed
// name is always a complete file, and an in-progress tmp_obj_* is skipped
// rather than copied half-written. No locking is needed.
//
// Copies never clobber. Object names are content addresses, so a name already
// present is the same bytes, and rewriting it would be pure risk.
// It reads the store through an os.Root, which refuses symlink and ".."
// escapes. That is not belt-and-braces: the store is written by a process
// inside the container, and on a Linux host — including the remote-daemon case
// — a symlink planted there resolves on the HOST when the daemon follows it.
// Measured: `ln -s /etc/passwd …` inside the container succeeded on the bind
// mount, and a host-side reader printed the host's own file. On Windows the
// same link is an inert reparse point, so a Windows-only test never sees it.
func HarvestObjects(m Mapping) (copied int, err error) {
	src := m.HostObjects()
	dst := joinHost(m.HostGitCommon, "objects")

	root, err := os.OpenRoot(src)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sandbox: open object store: %w", err)
	}
	defer root.Close()

	entries, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sandbox: read object store: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && isShardDir(name):
			n, err := harvestShard(root, name, filepath.Join(dst, name))
			copied += n
			if err != nil {
				return copied, err
			}
		case e.IsDir() && name == "pack":
			n, err := harvestPack(root, name, filepath.Join(dst, name))
			copied += n
			if err != nil {
				return copied, err
			}
		}
		// Everything else — info/ above all — is deliberately skipped.
	}
	return copied, nil
}

// isShardDir reports the two-hex-character directory names git sorts loose
// objects into.
func isShardDir(name string) bool { return len(name) == 2 && isHex(name) }

// harvestShard copies the well-formed loose objects out of one shard.
//
// Reads go through the root, so a shard the agent replaced with a symlink to
// somewhere else on the host resolves to nothing rather than to that place.
func harvestShard(root *os.Root, shard, dst string) (int, error) {
	entries, err := readDirIn(root, shard)
	if err != nil {
		return 0, fmt.Errorf("sandbox: read shard: %w", err)
	}
	var copied int
	for _, e := range entries {
		// A loose object's name is the remaining 38 hex characters of its
		// sha1 (or 62 of its sha256). Anything else in here is git's own
		// in-flight temp file.
		n := e.Name()
		if e.IsDir() || !isHex(n) || (len(n) != 38 && len(n) != 62) {
			continue
		}
		ok, err := copyNoClobber(root, shard+"/"+n, filepath.Join(dst, n))
		if err != nil {
			return copied, err
		}
		if ok {
			copied++
		}
	}
	return copied, nil
}

// harvestPack copies pack files. Container-side `git repack -a -d` succeeds
// even though `git gc` does not — it takes no gc.pid.lock — so packs are a
// real case, not a theoretical one.
func harvestPack(root *os.Root, dir, dst string) (int, error) {
	entries, err := readDirIn(root, dir)
	if err != nil {
		return 0, fmt.Errorf("sandbox: read pack dir: %w", err)
	}
	var copied int
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "pack-") {
			continue
		}
		if !strings.HasSuffix(n, ".pack") && !strings.HasSuffix(n, ".idx") && !strings.HasSuffix(n, ".rev") {
			continue
		}
		ok, err := copyNoClobber(root, dir+"/"+n, filepath.Join(dst, n))
		if err != nil {
			return copied, err
		}
		if ok {
			copied++
		}
	}
	return copied, nil
}

// readDirIn lists one directory inside the rooted tree.
func readDirIn(root *os.Root, name string) ([]os.DirEntry, error) {
	d, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(-1)
}

// copyNoClobber copies rel (inside root) to dst unless dst exists, reporting
// whether it wrote. It writes to a temp file and renames, so a harvest
// interrupted half-way never leaves a truncated object under a name git will
// trust.
//
// The SOURCE is opened through the root and the DESTINATION is not: the source
// is agent-writable and the destination is the user's own repository, which
// this process owns.
func copyNoClobber(root *os.Root, rel, dst string) (bool, error) {
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, fmt.Errorf("sandbox: create object dir: %w", err)
	}
	in, err := root.Open(rel)
	if err != nil {
		// The container may have repacked this object away between the
		// readdir and here, or replaced it with a link the root refuses.
		// Neither is a failure worth stopping the pass for: a repacked
		// object arrives in the pack this same pass harvests, and a refused
		// link is exactly the outcome the root exists to produce.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("sandbox: open object: %w", err)
	}
	defer in.Close()

	tmp := dst + ".quiltmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o444)
	if err != nil {
		return false, fmt.Errorf("sandbox: create object: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return false, fmt.Errorf("sandbox: copy object: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("sandbox: close object: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		// Another harvest, or the host's own git, may have created it in
		// the window. Same content either way.
		if _, statErr := os.Stat(dst); statErr == nil {
			return false, nil
		}
		return false, fmt.Errorf("sandbox: rename object: %w", err)
	}
	return true, nil
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return s != ""
}
