package expression

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHashFilesWithoutCapabilityNotSupported(t *testing.T) {
	_, err := Eval("hashFiles('**/*.lock')", NewEnv())
	var ns *NotSupportedError
	if !errors.As(err, &ns) {
		t.Fatalf("err = %v, want NotSupportedError", err)
	}
}

func TestHashFilesArityAndTypes(t *testing.T) {
	env := NewEnv().WithWorkspaceHasher(fakeHasher{})
	cases := []string{
		"hashFiles()",
		"hashFiles(1)",
		"hashFiles('a', 'b', null)",
	}
	for _, expr := range cases {
		if _, err := Eval(expr, env); err == nil {
			t.Errorf("%s: expected error", expr)
		}
	}
}

type fakeHasher struct{ result string }

func (f fakeHasher) HashFiles(...string) (string, error) { return f.result, nil }

// The capability flows through With-copies and Interpolate.
func TestHashFilesCapabilityInjected(t *testing.T) {
	env := NewEnv().With("github", ObjOf(map[string]Value{
		"sha": StrOf("abc"),
	})).WithWorkspaceHasher(fakeHasher{result: "cafebabe"})
	child := env.With("env", ObjOf(map[string]Value{"K": StrOf("v")}))

	got, err := Interpolate("lock=${{ hashFiles('go.sum') }}", child)
	if err != nil || got != "lock=cafebabe" {
		t.Fatalf("interpolate = %q err=%v", got, err)
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const (
	lockA = "a: 1\n"
	lockB = "b: 2\n"
	srcGo = "package main\n"
)

func TestDirHasherSingleFile(t *testing.T) {
	root := writeTree(t, map[string]string{"go.sum": lockA})
	sum, err := NewDirHasher(root).HashFiles("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(lockA))
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("sum = %s", sum)
	}
}

func TestDirHasherSortedOrderDeterministic(t *testing.T) {
	files := map[string]string{"b/x.lock": lockB, "a/y.lock": lockA, "src.go": srcGo}
	h1 := NewDirHasher(writeTree(t, files))
	h2 := NewDirHasher(writeTree(t, files)) // same contents, fresh mtimes

	s1, err := h1.HashFiles("**/*.lock")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := h2.HashFiles("**/*.lock")
	if err != nil {
		t.Fatal(err)
	}
	if s1 == "" || s1 != s2 {
		t.Fatalf("unstable hash: %s vs %s", s1, s2)
	}

	digest := sha256.New()
	digest.Write([]byte(lockA))
	digest.Write([]byte(lockB))
	if want := hex.EncodeToString(digest.Sum(nil)); s1 != want {
		t.Errorf("hash = %s, want sorted-content %s", s1, want)
	}
}

func TestDirHasherGlobSemantics(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.lock":         lockA,
		"sub/b.lock":     lockB,
		"sub/deep/c.txt": "c",
	})
	h := NewDirHasher(root)

	single := func(patterns ...string) string {
		t.Helper()
		got, err := h.HashFiles(patterns...)
		if err != nil {
			t.Fatalf("%v: %v", patterns, err)
		}
		return got
	}
	digestOf := func(contents ...string) string {
		digest := sha256.New()
		for _, c := range contents {
			digest.Write([]byte(c))
		}
		return hex.EncodeToString(digest.Sum(nil))
	}

	// `*` does not cross segments.
	if got := single("*.lock"); got != digestOf(lockA) {
		t.Errorf("*.lock = %s", got)
	}
	// `**` spans zero or more segments.
	if got := single("**/*.lock"); got != digestOf(lockA, lockB) {
		t.Errorf("**/*.lock = %s", got)
	}
	// `**` in the middle and after prefixes.
	if got := single("sub/**/*.txt"); got != digestOf("c") {
		t.Errorf("sub/**/*.txt = %s", got)
	}
	if got := single("**/deep/**"); got != digestOf("c") {
		t.Errorf("**/deep/** = %s", got)
	}
	// Single-char and class segments stay within one level.
	if got := single("?.lock"); got != digestOf(lockA) {
		t.Errorf("?.lock = %s", got)
	}
	if got := single("[ab].lock"); got != digestOf(lockA) {
		t.Errorf("[ab].lock = %s", got)
	}
	// Multi-pattern union deduplicates and keeps sorted order.
	if got := single("a.lock", "**/*.lock"); got != digestOf(lockA, lockB) {
		t.Errorf("union = %s", got)
	}
	// No match hashes to "".
	if got := single("nomatch/*.x"); got != "" {
		t.Errorf("no match = %q, want empty", got)
	}
}

func TestDirHasherContentSensitivity(t *testing.T) {
	root := writeTree(t, map[string]string{"f.lock": lockA})
	h := NewDirHasher(root)
	before, err := h.HashFiles("f.lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.lock"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := h.HashFiles("f.lock")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("hash did not change with content")
	}
}

var _ WorkspaceHasher = (*DirHasher)(nil)
