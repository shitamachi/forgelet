package filecommand

import (
	"testing"
)

func TestParseKVSimple(t *testing.T) {
	kvs, err := ParseKVFile([]byte("FOO=bar\nEMPTY=\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kvs) != 2 || kvs[0].Key != "FOO" || kvs[0].Value != "bar" || kvs[1].Value != "" {
		t.Errorf("kvs = %+v", kvs)
	}
}

func TestParseKVHeredoc(t *testing.T) {
	kvs, err := ParseKVFile([]byte("MULTI<<EOF\nline one\nline two\nEOF\nAFTER=1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kvs) != 2 {
		t.Fatalf("kvs = %+v", kvs)
	}
	if kvs[0].Key != "MULTI" || kvs[0].Value != "line one\nline two" {
		t.Errorf("heredoc value = %+v", kvs[0])
	}
	if kvs[1].Key != "AFTER" || kvs[1].Value != "1" {
		t.Errorf("trailing pair = %+v", kvs[1])
	}
}

func TestParseKVErrors(t *testing.T) {
	if _, err := ParseKVFile([]byte("noequalsign\n")); err == nil {
		t.Error("line without '=' must fail")
	}
	if _, err := ParseKVFile([]byte("KEY<<EOF\nnever closed\n")); err == nil {
		t.Error("unterminated heredoc must fail")
	}
}

func TestParseKVLastWins(t *testing.T) {
	env := Apply(map[string]string{}, mustParse(t, "X=1\nX=2\n"))
	if env["X"] != "2" {
		t.Errorf("X = %q, want 2", env["X"])
	}
}

func mustParse(t *testing.T, s string) []KV {
	t.Helper()
	kvs, err := ParseKVFile([]byte(s))
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return kvs
}

func TestParsePathFile(t *testing.T) {
	paths := ParsePathFile([]byte("/a/b\n\n/c d/e\n\n"))
	if len(paths) != 2 || paths[0] != "/a/b" || paths[1] != "/c d/e" {
		t.Errorf("paths = %+v", paths)
	}
	if got := ParsePathFile(nil); got != nil {
		t.Errorf("nil input = %+v", got)
	}
}
