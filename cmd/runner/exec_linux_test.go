//go:build linux

package main

import "testing"

func TestValidEnvKeyRejectsWhatAShellCannotCarry(t *testing.T) {
	for _, key := range []string{"DOCKER_HOST", "PATH", "_X1"} {
		if !validEnvKey(key) {
			t.Fatalf("%q was rejected", key)
		}
	}
	for _, key := range []string{"", "A=B", "A B", "A-B", "PATH\n"} {
		if validEnvKey(key) {
			t.Fatalf("%q was accepted", key)
		}
	}
}
