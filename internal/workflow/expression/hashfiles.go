package expression

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// WorkspaceHasher is the explicit capability behind hashFiles() (FR-3.4):
// the engine never touches the filesystem itself; callers inject an
// implementation rooted at the job workspace.
type WorkspaceHasher interface {
	// HashFiles returns the combined SHA-256 of all files matching the
	// given glob patterns relative to the workspace, or "" when nothing
	// matches. Errors surface as EvalError to callers.
	HashFiles(patterns ...string) (string, error)
}

// WithWorkspaceHasher registers h for this evaluation phase. Scheduler-time
// evaluation simply does not call it: hashFiles() without a registered
// capability yields NotSupportedError instead of touching any filesystem.
func (e *Env) WithWorkspaceHasher(h WorkspaceHasher) *Env {
	next := e.With(hasherContextKey, StrOf("registered"))
	next.hasher = h
	return next
}

const hasherContextKey = "__workspacehasher"

// hashFiles evaluates hashFiles(pattern...) through the injected capability.
func hashFilesFn(env *Env, args []Value) (Value, error) {
	if len(args) == 0 {
		return Null, &EvalError{Msg: "hashFiles() takes at least one pattern"}
	}
	patterns := make([]string, 0, len(args))
	for _, a := range args {
		if a.Kind != KindString {
			return Null, &EvalError{Msg: "hashFiles(): patterns must be strings"}
		}
		if strings.Contains(a.Str, "\x00") {
			return Null, &EvalError{Msg: "hashFiles(): invalid pattern"}
		}
		patterns = append(patterns, a.Str)
	}
	if env.hasher == nil {
		return Null, &NotSupportedError{What: "function hashFiles() outside a workspace context"}
	}
	sum, err := env.hasher.HashFiles(patterns...)
	if err != nil {
		return Null, &EvalError{Msg: "hashFiles(): " + err.Error()}
	}
	return StrOf(sum), nil
}

// DirHasher hashes files under Root following GitHub hashFiles semantics:
// patterns are slash-separated globs (`*`, `?`, `[class]`, and `**` across
// segments), matches are deduplicated and processed in sorted path order,
// and an empty match set hashes to "".
//
// It is the adapter for executor-side evaluation; the pure engine only sees
// the WorkspaceHasher interface.
type DirHasher struct {
	Root string
}

// NewDirHasher wires a DirHasher rooted at the job workspace directory.
func NewDirHasher(root string) *DirHasher { return &DirHasher{Root: root} }

// HashFiles implements WorkspaceHasher.
func (d *DirHasher) HashFiles(patterns ...string) (string, error) {
	matches := map[string]bool{}
	err := fs.WalkDir(os.DirFS(d.Root), ".", func(p string, entry fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		for _, pat := range patterns {
			ok, err := matchGlob(pat, p)
			if err != nil {
				return fmt.Errorf("pattern %q: %w", pat, err)
			}
			if ok {
				matches[p] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	files := make([]string, 0, len(matches))
	for f := range matches {
		files = append(files, f)
	}
	sort.Strings(files)
	digest := sha256.New()
	for _, f := range files {
		file, err := os.Open(filepath.Join(d.Root, filepath.FromSlash(f)))
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(digest, file); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// matchGlob reports whether the slash-separated rel path matches pattern.
// `**` spans zero or more whole segments; other segments use path.Match
// syntax (`*`/`?`/`[...]`, never crossing `/`).
func matchGlob(pattern, name string) (bool, error) {
	pat := strings.Split(strings.Trim(pattern, "/"), "/")
	seg := strings.Split(strings.Trim(name, "/"), "/")
	return matchSegments(pat, seg)
}

func matchSegments(pat, seg []string) (bool, error) {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Try consuming zero segments first, then one more each time.
			for i := 0; i <= len(seg); i++ {
				ok, err := matchSegments(pat[1:], seg[i:])
				if err == nil && ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(seg) == 0 {
			return false, nil
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false, err
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0, nil
}
