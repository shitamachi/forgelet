package runner

import "testing"

func TestRunningBuildPlatforms(t *testing.T) {
	for _, tc := range []struct{ name, output, want string }{
		{"running", "Status: running\nPlatforms: linux/arm64, linux/amd64*, linux/386\n", "linux/amd64,linux/arm64"},
		{"stopped", "Status: stopped\nPlatforms: linux/amd64*\n", "none"},
		{"multiple nodes", "Status: stopped\nPlatforms: linux/arm64\nStatus: running\nPlatforms: linux/amd64,linux/amd64/v2\n", "linux/amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runningBuildPlatforms(tc.output); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
