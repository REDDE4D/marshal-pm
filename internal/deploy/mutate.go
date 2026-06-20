package deploy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/config"
	"marshal/internal/pb"
)

// confineNew is confine's sibling for paths that may not exist yet (create /
// rename destination). It does the same lexical containment check, then resolves
// the deepest *existing* ancestor via EvalSymlinks and verifies that stays inside
// the real root, defeating a symlinked parent directory. Returns generic errors.
func confineNew(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	full := filepath.Clean(filepath.Join(root, rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("deploy root unavailable")
	}
	// Walk up to the deepest existing ancestor and resolve it.
	anc := full
	var tail []string
	for {
		if anc == root {
			break
		}
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		tail = append([]string{filepath.Base(anc)}, tail...)
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	realAnc, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return "", fmt.Errorf("path escapes deploy root")
	}
	if realAnc != realRoot && !strings.HasPrefix(realAnc, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root via symlink")
	}
	return filepath.Join(append([]string{realAnc}, tail...)...), nil
}

// isGitInternal reports whether rel addresses the clone's own .git directory.
func isGitInternal(rel string) bool {
	clean := filepath.Clean(filepath.FromSlash(rel))
	parts := strings.Split(clean, string(filepath.Separator))
	return len(parts) > 0 && parts[0] == ".git"
}

// gitOut runs a git command capturing stdout; on failure it returns a generic,
// non-path-leaking error keyed on the subcommand.
func gitOut(r Runner, dir string, env []string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	if err := r.Run(context.Background(), dir, env, &out, &errb, "git", args...); err != nil {
		verb := "git"
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				verb = a
				break
			}
		}
		return "", fmt.Errorf("git %s failed", verb)
	}
	return strings.TrimSpace(out.String()), nil
}

// gitIdentity derives the commit author from the credential, with a fixed
// fallback when the app has no managed credential.
func gitIdentity(cred Credential) (name, email string) {
	if cred.Username != "" {
		return cred.Username, cred.Username + "@marshal.local"
	}
	return "Marshal", "marshal@localhost"
}

// mutateAndPush applies one file mutation in dir's clone, commits it with an
// inline identity, and pushes to origin. On any failure after HEAD is captured
// it rolls the working tree back to the pre-op commit. Never force-pushes,
// never stages anything but the affected path(s).
func (d *Deployer) mutateAndPush(dir string, src config.GitSource, cred Credential, kind pb.CommitKind, rel, newRel string, content []byte, message string) (*pb.CommitResult, error) {
	// Branch gate: commit+push only makes sense onto a branch.
	branch, err := gitOut(d.runner, dir, nil, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil || branch == "" {
		return nil, fmt.Errorf("deployment is not on a branch (read-only)")
	}
	if message == "" {
		message = "Update via Marshal"
	}

	// Resolve + guard target path(s).
	if isGitInternal(rel) || (kind == pb.CommitKind_COMMIT_RENAME && isGitInternal(newRel)) {
		return nil, fmt.Errorf("cannot modify .git")
	}
	var leftover string // untracked path to remove on rollback (create/rename dest)

	preSHA, err := gitOut(d.runner, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse failed")
	}

	apply := func() error {
		switch kind {
		case pb.CommitKind_COMMIT_EDIT:
			full, err := confine(dir, rel)
			if err != nil {
				return err
			}
			if err := os.WriteFile(full, content, 0o644); err != nil {
				return fmt.Errorf("write failed")
			}
			_, err = gitOut(d.runner, dir, nil, "add", "--", rel)
			return err
		case pb.CommitKind_COMMIT_CREATE:
			full, err := confineNew(dir, rel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return fmt.Errorf("mkdir failed")
			}
			if err := os.WriteFile(full, content, 0o644); err != nil {
				return fmt.Errorf("write failed")
			}
			leftover = full
			_, err = gitOut(d.runner, dir, nil, "add", "--", rel)
			return err
		case pb.CommitKind_COMMIT_DELETE:
			if _, err := confine(dir, rel); err != nil {
				return err
			}
			_, err := gitOut(d.runner, dir, nil, "rm", "--", rel)
			return err
		case pb.CommitKind_COMMIT_RENAME:
			if _, err := confine(dir, rel); err != nil {
				return err
			}
			full, err := confineNew(dir, newRel)
			if err != nil {
				return err
			}
			leftover = full
			_, err = gitOut(d.runner, dir, nil, "mv", "--", rel, newRel)
			return err
		default:
			return fmt.Errorf("unknown commit kind")
		}
	}

	rollback := func() {
		_, _ = gitOut(d.runner, dir, nil, "reset", "--hard", preSHA)
		if leftover != "" {
			_ = os.Remove(leftover)
		}
	}

	if err := apply(); err != nil {
		rollback()
		return nil, err
	}

	name, email := gitIdentity(cred)
	if _, err := gitOut(d.runner, dir, nil,
		"-c", "credential.helper=", "-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "-m", message); err != nil {
		rollback()
		return nil, fmt.Errorf("git commit failed")
	}

	env, cleanup, err := d.gitCredEnv(cred)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("credential setup failed")
	}
	defer cleanup()
	credActive := cred.Token != ""
	pushURL := "origin"
	if credActive {
		pushURL = withUsername(src.Repo, cred.Username)
	}
	if _, err := gitOut(d.runner, dir, env,
		gitArgs(credActive, "push", pushURL, "HEAD:refs/heads/"+branch)...); err != nil {
		rollback()
		return nil, fmt.Errorf("push rejected (origin moved or credential lacks write access)")
	}

	sha, _ := gitOut(d.runner, dir, nil, "rev-parse", "HEAD")
	return &pb.CommitResult{Sha: sha, Branch: branch}, nil
}
