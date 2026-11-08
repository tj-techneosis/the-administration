package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The house from a trusted ref (place --house-from REF): the professionals,
// roster, handouts and hooks as they are at REF, not as the checkout under
// review has them. A branch can rewrite its own house (its critics' briefs,
// its hooks), so a review of a branch someone else pushed takes the house
// from a ref the person trusts, such as origin/main. It's REF's own
// .administration when REF has one; else the house kept on this machine
// for the project's origin, which no branch can change; never the
// checkout's. REF's is unpacked once per commit into the books (houses/),
// by git archive with the repository's filters off, files and folders only.

// keptRef is --house-from's word for the house kept on this machine.
const keptRef = ":kept"

// houseFrom is the house for a contract placed with --house-from ref, and
// the commit it was taken from ("" for a house kept on the machine). A dry
// run (scratch) unpacks into a folder of its own that done removes, so it
// leaves nothing in the books.
func houseFrom(project, ref string, scratch bool) (dir, sha, name string, done func(), err error) {
	done = func() {}
	dir, sha, name, err = houseFromAt(project, ref, scratch, &done)
	return dir, sha, name, done, err
}

// fullSHARE is a whole commit id (SHA-1 or SHA-256).
var fullSHARE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// resolveHouseRef turns --house-from's REF into a commit, and says which
// ref it came from. Whoever controls the reviewed repository can push
// tags and branches into this clone, so a name is never searched for
// across kinds of ref (git's own lookup takes a tag before a remote
// branch of the same name, and takes whatever exists when the one meant
// doesn't). Each form means one thing:
//   - a full commit id is that commit;
//   - refs/... is that ref and nothing else;
//   - REMOTE/BRANCH (REMOTE a configured remote) is refs/remotes/REMOTE/BRANCH;
//   - REMOTE alone is the branch its refs/remotes/REMOTE/HEAD points to.
//
// Anything else (a bare name, a short id, HEAD) is refused with what to
// say instead.
func resolveHouseRef(project, ref string) (sha, name string, err error) {
	bad := func(why string) (string, string, error) {
		return "", "", usageError{fmt.Sprintf("--house-from %s: %s", ref, why)}
	}
	if _, err := git(project, "rev-parse", "--git-dir"); err != nil {
		return bad(project + " isn't in a git repository")
	}
	// Every commit asked for here is a full id, never a name, so nothing
	// the repository holds can stand in for it.
	commit := func(oid string) (string, bool) {
		out, err := git(project, "rev-parse", "--verify", "--quiet", oid+"^{commit}")
		return out, err == nil && out != ""
	}
	exact := func(full string) (string, bool) {
		out, err := git(project, "show-ref", "--verify", "--hash", "--", full)
		return out, err == nil && out != ""
	}
	fromRef := func(full string) (string, string, error) {
		if _, err := git(project, "check-ref-format", full); err != nil {
			return bad("not a valid ref name")
		}
		oid, ok := exact(full)
		if !ok {
			return bad(fmt.Sprintf("no such ref as %s here (fetch it first?)", full))
		}
		if sha, ok := commit(oid); ok {
			return sha, full, nil
		}
		return bad(full + " isn't a commit")
	}
	lower := strings.ToLower(ref)
	switch {
	case fullSHARE.MatchString(lower):
		if sha, ok := commit(lower); ok && sha == lower {
			return sha, sha, nil
		}
		return bad("no such commit here (fetch it first?)")
	case strings.HasPrefix(ref, "refs/"):
		return fromRef(ref)
	}
	remotes, _ := git(project, "remote")
	for _, r := range strings.Fields(remotes) {
		if ref == r {
			// The remote's default branch, named, so the person sees which.
			target, err := git(project, "symbolic-ref", "-q", "refs/remotes/"+r+"/HEAD")
			if err != nil || !strings.HasPrefix(target, "refs/remotes/"+r+"/") {
				return bad(fmt.Sprintf("this clone doesn't know %s's default branch (refs/remotes/%s/HEAD); name it: %s/<branch>", r, r, r))
			}
			return fromRef(target)
		}
		if strings.HasPrefix(ref, r+"/") && len(ref) > len(r)+1 {
			return fromRef("refs/remotes/" + ref)
		}
	}
	switch {
	case ref == "HEAD" || strings.HasSuffix(ref, "_HEAD") || strings.Contains(ref, "@"):
		return bad("that's the checkout under review, or a step from it; name a ref you trust: <remote>/<branch>, refs/tags/<tag>, or a full commit id")
	case hexRE.MatchString(lower):
		return bad("a short commit id could be shadowed by a ref of that name; give the full commit id")
	}
	return bad("say which ref you mean: <remote>/<branch> (origin/main), refs/heads/<branch>, refs/tags/<tag>, or a full commit id")
}

func houseFromAt(project, ref string, scratch bool, done *func()) (dir, sha, name string, err error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", "", "", usageError{fmt.Sprintf("--house-from %q: give a branch, tag or commit", ref)}
	}
	// ":kept": the house kept on this machine, as a contract whose ref
	// had none recorded it (a resume of one passes it on). No ref's name
	// has a colon, so it can't be taken for one.
	if ref == keptRef {
		if kept := keptHouse(project); kept != "" && isDir(kept) {
			return realPath(kept), "", keptRef, nil
		}
		return "", "", "", usageError{fmt.Sprintf("--house-from %s: no house is kept on this machine for %s", keptRef, project)}
	}
	sha, name, err = resolveHouseRef(project, ref)
	if err != nil {
		return "", "", "", err
	}
	prefix, err := git(project, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", "", err
	}
	inRepo := path.Join(filepath.ToSlash(prefix), ".administration")
	top, err := git(project, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", "", err
	}
	// Only a folder is a house. A link or a submodule there is refused, not
	// read as no house: falling back to another without a word would hide
	// what REF has.
	switch t, err := git(top, "cat-file", "-t", sha+":"+inRepo); {
	case err != nil:
		if kept := keptHouse(project); kept != "" && isDir(kept) {
			return realPath(kept), "", keptRef, nil
		}
		return "", "", "", usageError{fmt.Sprintf("--house-from %s: %s has no %s, and no house is kept on this machine for this project", ref, short(sha), inRepo)}
	case t != "tree":
		return "", "", "", usageError{fmt.Sprintf("--house-from %s: %s at %s is a %s (a link or a submodule?), not a folder; a house from a ref is a folder in it", ref, inRepo, short(sha), t)}
	}
	books, err := booksDir()
	if err != nil {
		return "", "", "", err
	}
	h := sha256.Sum256([]byte(sha + "\x00" + inRepo))
	dir = filepath.Join(books, "houses", hex.EncodeToString(h[:8]))
	if isDir(dir) {
		// Reused: its time says so, so prune leaves it while this place
		// gets as far as writing the contract that names it.
		if !scratch {
			now := time.Now()
			_ = os.Chtimes(dir, now, now)
		}
		return dir, sha, name, nil
	}
	if scratch {
		tmp, err := os.MkdirTemp("", "administration-house-")
		if err != nil {
			return "", "", "", err
		}
		if err := unpackHouse(top, sha, inRepo, tmp); err != nil {
			os.RemoveAll(tmp)
			return "", "", "", fmt.Errorf("--house-from %s: %v", ref, err)
		}
		*done = func() { os.RemoveAll(tmp) }
		return tmp, sha, name, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", "", "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".unpacking-")
	if err != nil {
		return "", "", "", err
	}
	defer os.RemoveAll(tmp)
	if err := unpackHouse(top, sha, inRepo, tmp); err != nil {
		return "", "", "", fmt.Errorf("--house-from %s: %v", ref, err)
	}
	// Another place may have unpacked the same commit meanwhile: theirs is
	// the same files, and stays.
	if err := os.Rename(tmp, dir); err != nil && !isDir(dir) {
		return "", "", "", err
	}
	return dir, sha, name, nil
}

// unpackHouse writes the commit's house folder into dst: regular files
// (keeping whether each is executable) and folders. A link or anything
// else is refused rather than followed: a house from a ref is read, and a
// link could point anywhere on the machine.
// It runs from the repository's top, where inRepo's path is.
func unpackHouse(top, sha, inRepo, dst string) error {
	cmd := gitCmd(top, "archive", "--format=tar", sha, "--", inRepo)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return err
	}
	readErr := func() error {
		tr := tar.NewReader(out)
		for {
			hd, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			name := path.Clean(hd.Name)
			if hd.Typeflag == tar.TypeXGlobalHeader {
				continue
			}
			// The folders above the house come first, from the top: passed
			// over. Anything else outside it is refused.
			if hd.Typeflag == tar.TypeDir && strings.HasPrefix(inRepo, name+"/") {
				continue
			}
			rel := strings.TrimPrefix(name, inRepo)
			if rel == name || (rel != "" && !strings.HasPrefix(rel, "/")) {
				return fmt.Errorf("%s is outside %s", hd.Name, inRepo)
			}
			rel = strings.TrimPrefix(rel, "/")
			if rel == "" {
				continue
			}
			if !filepath.IsLocal(rel) {
				return fmt.Errorf("%s leaves the house", hd.Name)
			}
			to := filepath.Join(dst, filepath.FromSlash(rel))
			switch hd.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(to, 0o700); err != nil {
					return err
				}
			case tar.TypeReg:
				if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
					return err
				}
				mode := os.FileMode(0o600)
				if hd.Mode&0o111 != 0 {
					mode = 0o700
				}
				f, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
				if err != nil {
					return err
				}
				_, err = io.Copy(f, tr)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s isn't a file or a folder (a link?); a house from a ref has only those", rel)
			}
		}
	}()
	// Drain whatever is left so git can finish, then wait for it.
	_, _ = io.Copy(io.Discard, out)
	werr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	if werr != nil {
		return fmt.Errorf("git archive: %s", strings.TrimSpace(errb.String()))
	}
	return nil
}
