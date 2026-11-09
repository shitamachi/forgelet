package run

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

const ExternalLease = 60 * time.Second

// Runner labels name an execution environment a device provides. A job's
// runs-on profile selects the devices that may claim it.
const (
	MacRunnerLabel  = "macos-arm64"
	WSLCRunnerLabel = "linux-amd64-wslc"
)

// RunnerLabelsHeader announces the runs-on profiles a device can serve. Its
// absence marks a client that predates capability reporting, which keeps the
// original shared-queue behavior.
const RunnerLabelsHeader = "X-Forgelet-Runner-Labels"

var externalRunnerLabels = []string{MacRunnerLabel, WSLCRunnerLabel}

// ExternalRunnerLabel reports whether label has a built-in device backend.
func ExternalRunnerLabel(label string) bool {
	return slices.Contains(externalRunnerLabels, label)
}

// DeviceLabelMismatch reports whether a job's runs-on label names a device
// backend that a device with these capabilities cannot serve. Portable labels
// keep the original shared-queue behavior, where any capable device may claim
// the job.
func DeviceLabelMismatch(label string, capabilities []string) bool {
	return ExternalRunnerLabel(label) && len(capabilities) > 0 && !slices.Contains(capabilities, label)
}

// ExternalRunnerLabels lists the built-in device backends.
func ExternalRunnerLabels() []string {
	return slices.Clone(externalRunnerLabels)
}

// ExternalRunnerDescription describes a built-in device backend for the console.
func ExternalRunnerDescription(label string) string {
	switch label {
	case MacRunnerLabel:
		return "macOS arm64"
	case WSLCRunnerLabel:
		return "Windows WSLC (Linux amd64)"
	}
	return ""
}

// RunnerLabels parses a capability header into distinct profile labels. Custom
// labels are portable profiles an administrator mapped to a device; invalid or
// oversized entries are dropped rather than widening a device's capabilities.
func RunnerLabels(header string) []string {
	if header == "" {
		return nil
	}
	labels := []string{}
	for _, label := range strings.Split(header, ",") {
		label = strings.TrimSpace(label)
		if !ValidRunnerLabel(label) || slices.Contains(labels, label) {
			continue
		}
		labels = append(labels, label)
		if len(labels) == 8 {
			break
		}
	}
	return labels
}

// ValidRunnerLabel matches the profile names a plan may use as runs-on.
func ValidRunnerLabel(label string) bool {
	if label == "" || len(label) > 64 || label[0] == '-' {
		return false
	}
	for _, r := range label {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func (j Job) ExternalRunnerID() string {
	if j.RunnerID == "" && j.Plan.External && j.AttemptID != "" {
		return j.Profile // Before the shared queue, the label was the device ID.
	}
	return j.RunnerID
}

// ExternalAttempt identifies a grant without exposing the bearer capability.
func ExternalAttempt(token string) string {
	value, ok := strings.CutPrefix(token, "frja_")
	if !ok || len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:16])
}

type ExternalGrant struct {
	JobID      string `json:"job_id"`
	RunID      string `json:"run_id"`
	Repository string `json:"repository"`
	Name       string `json:"name"`
	SHA        string `json:"sha"`
	Platform   string `json:"platform,omitempty"`
	Seconds    int    `json:"lease_seconds"`
}

// RunnerInfo is the small, self-reported description of a device. A device
// reads it from the operating system at most every 30 seconds; anything that
// would cost more than a single system call is left out on purpose, because the
// machine usually has other work to do.
type RunnerInfo struct {
	OS                   string    `json:"os,omitempty"`
	Hostname             string    `json:"hostname,omitempty"`
	Arch                 string    `json:"arch,omitempty"`
	Version              string    `json:"version,omitempty"`
	Backend              string    `json:"backend,omitempty"` // native or wslc.
	Engine               string    `json:"engine,omitempty"`  // e.g. the wslc version.
	CPUs                 int       `json:"cpus,omitempty"`
	MemoryTotalBytes     uint64    `json:"memory_total_bytes,omitempty"`
	MemoryAvailableBytes uint64    `json:"memory_available_bytes,omitempty"`
	Load1                float64   `json:"load1,omitempty"`
	ReportedAt           time.Time `json:"reported_at,omitzero"`
}

// Score is the device's current pressure, lower being more room to work: the
// larger of CPU pressure (one-minute load per processor) and memory pressure.
// Memory only counts once a quarter of it is in use, because an ordinary idle
// machine holding caches is not short of room; beyond that it rises to 1 when
// the device is full. Unknown readings do not count against a device, and the
// result is clamped so a misreport cannot dominate scheduling.
func (i RunnerInfo) Score() float64 {
	cpus := i.CPUs
	if cpus <= 0 {
		cpus = 1
	}
	score := 0.0
	if i.Load1 > 0 {
		score = i.Load1 / float64(cpus)
	}
	if i.MemoryTotalBytes > 0 && i.MemoryAvailableBytes <= i.MemoryTotalBytes {
		used := 1 - float64(i.MemoryAvailableBytes)/float64(i.MemoryTotalBytes)
		score = max(score, min(1, max(0, (used-0.25)/0.75)))
	}
	return min(1, max(0, score))
}

// ValidBounds is the range a device report must stay within to be stored.
func (i RunnerInfo) ValidBounds() bool {
	return i.CPUs >= 0 && i.CPUs <= 4096 && i.MemoryTotalBytes <= 1<<50 && i.MemoryAvailableBytes <= i.MemoryTotalBytes &&
		i.Load1 >= 0 && i.Load1 <= 1e7 && len(i.OS) <= 32 && len(i.Arch) <= 32 && len(i.Version) <= 64 && len(i.Hostname) <= 64 &&
		len(i.Backend) <= 32 && len(i.Engine) <= 64
}
