//go:build windows

package wsl

import (
	"slices"
	"testing"
)

func TestDecodeUTF16HandlesListOutput(t *testing.T) {
	// wsl --list --quiet prints UTF-16LE with CRLF line endings.
	text := "Ubuntu-24.04\r\narchlinux\r\n"
	encoded := []byte{0xff, 0xfe}
	for _, unit := range []rune(text) {
		encoded = append(encoded, byte(unit), byte(unit>>8))
	}
	got := decodeUTF16(encoded)
	if got != text {
		t.Fatalf("decode = %q", got)
	}
	// Command output is ordinary UTF-8 and must pass through unchanged.
	if got := decodeUTF16([]byte("archlinux\n")); got != "archlinux\n" {
		t.Fatalf("utf-8 decode = %q", got)
	}
}

func TestRunArgsUseTheSelectedDistribution(t *testing.T) {
	client := Client{Path: `C:\Windows\System32\wsl.exe`}
	for _, tc := range []struct {
		name         string
		distro, user string
		want         []string
	}{
		{"default", "", "", []string{"--exec", "/home/u/runner", "--exec"}},
		{"distro", "archlinux", "", []string{"--distribution", "archlinux", "--exec", "/home/u/runner", "--exec"}},
		{"user", "archlinux", "guo", []string{"--distribution", "archlinux", "--user", "guo", "--exec", "/home/u/runner", "--exec"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := client.RunArgs(tc.distro, tc.user, "/home/u/runner")
			if !slices.Equal(got, tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDisplayNamesTheDefaultDistribution(t *testing.T) {
	if display("") != "默认发行版" || display("archlinux") != "archlinux" {
		t.Fatal("unexpected display name")
	}
}
