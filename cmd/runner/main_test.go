package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSessionEndedOnlyCoversCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"canceled", context.Canceled, true},
		{"wrapped canceled", fmt.Errorf("pair device: %w", context.Canceled), true},
		{"deadline", context.DeadlineExceeded, true},
		{"refused", errors.New("控制面返回 HTTP 500"), false},
		{"nil", nil, false},
	} {
		if got := sessionEnded(tc.err); got != tc.want {
			t.Fatalf("%s: sessionEnded(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
