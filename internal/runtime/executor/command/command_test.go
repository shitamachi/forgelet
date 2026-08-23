package command

import "testing"

func TestParseMatrix(t *testing.T) {
	cases := []struct {
		line    string
		ok      bool
		name    string
		message string
		params  map[string]string
	}{
		{"::add-mask::super-secret", true, "add-mask", "super-secret", nil},
		{"::group::My group", true, "group", "My group", nil},
		{"::endgroup::", true, "endgroup", "", nil},
		{"::warning file=app.js,line=1::Something", true, "warning", "Something", map[string]string{"file": "app.js", "line": "1"}},
		{"::error::boom", true, "error", "boom", nil},
		{"::notice::fyi", true, "notice", "fyi", nil},
		{"::debug::trace", true, "debug", "trace", nil},
		{"::WARNING::upper", true, "warning", "upper", nil},
		{"ordinary output", false, "", "", nil},
		{":not-a-command", false, "", "", nil},
		{"::unknown-cmd::x", false, "", "", nil},
		{"::group no terminator", false, "", "", nil},
		{"::add-mask::with :: colons", true, "add-mask", "with :: colons", nil},
	}
	for _, tc := range cases {
		got, ok := Parse(tc.line)
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.line, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Name != tc.name || got.Message != tc.message {
			t.Errorf("%q: (%s, %q), want (%s, %q)", tc.line, got.Name, got.Message, tc.name, tc.message)
		}
		if len(got.Parameters) != len(tc.params) {
			t.Errorf("%q: params = %v, want %v", tc.line, got.Parameters, tc.params)
			continue
		}
		for k, v := range tc.params {
			if got.Parameters[k] != v {
				t.Errorf("%q: param %s = %q, want %q", tc.line, k, got.Parameters[k], v)
			}
		}
	}
}
