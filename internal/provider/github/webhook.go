// Package github adapts the GitHub provider: webhook verification and
// decoding, GitHub App credentials, and Check Run reporting. It is the only
// place forgelet talks to GitHub; everything downstream consumes the
// provider-neutral model types.
package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// ErrBadSignature marks webhook signature verification failures.
var ErrBadSignature = errors.New("github: invalid webhook signature")

// ErrIgnoredPush marks pushes that must not trigger a run (branch deletion).
var ErrIgnoredPush = errors.New("github: push ignored")

// ErrMalformedPayload marks undecodable event payloads.
var ErrMalformedPayload = errors.New("github: malformed payload")

// VerifySignature checks an X-Hub-Signature-256 header value against the
// request body. Comparison is constant time.
func VerifySignature(secret, body []byte, header string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return fmt.Errorf("%w: missing %q prefix", ErrBadSignature, prefix)
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return fmt.Errorf("%w: hex decode: %w", ErrBadSignature, err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return fmt.Errorf("%w: digest mismatch", ErrBadSignature)
	}
	return nil
}

// pushPayload carries the fields forgelet needs from a push event. It is a
// local projection, not a SDK type; the raw payload is persisted verbatim
// as the ProviderPayload for github.event construction.
type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

// DecodePush turns a push webhook payload into a provider-neutral event.
// Branch deletions return ErrIgnoredPush.
func DecodePush(body []byte, deliveryID string) (model.Event, error) {
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return model.Event{}, fmt.Errorf("%w: %w", ErrMalformedPayload, err)
	}
	if p.Deleted || isZeroSHA(p.After) {
		return model.Event{}, ErrIgnoredPush
	}
	if p.Ref == "" || p.After == "" || p.Repository.Name == "" || p.Repository.Owner.Login == "" {
		return model.Event{}, fmt.Errorf("%w: push payload missing ref/after/repository", ErrMalformedPayload)
	}
	actor := p.Pusher.Name
	if actor == "" {
		actor = "unknown"
	}
	return model.Event{
		Provider:   "github",
		Name:       "push",
		DeliveryID: deliveryID,
		Repository: model.RepositoryRef{Provider: "github", Owner: p.Repository.Owner.Login, Name: p.Repository.Name},
		Ref:        p.Ref,
		SHA:        p.After,
		Actor:      actor,
	}, nil
}

// PullRequestInfo summarizes a pull_request payload for trust decisions.
type PullRequestInfo struct {
	Action  string // payload action ("opened", "synchronize", ...)
	Fork    bool   // head came from a fork
	BaseRef string // base branch short name
	HeadRef string // head branch short name
}

type prPayload struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	Pr     struct {
		Head struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				Fork  bool   `json:"fork"`
				Name  string `json:"name"`
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			Repo struct {
				Name  string `json:"name"`
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
}

// Actions that trigger a run; closed/completed ones are ignored.
var prRunActions = map[string]bool{"opened": true, "synchronize": true, "reopened": true}

// DecodePull turns a pull_request payload into an event plus trust info.
// The event's repository/SHA/ref point at the PR head (what gets built).
func DecodePull(body []byte, deliveryID string) (model.Event, PullRequestInfo, error) {
	var p prPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return model.Event{}, PullRequestInfo{}, fmt.Errorf("%w: %w", ErrMalformedPayload, err)
	}
	if p.Action == "" {
		return model.Event{}, PullRequestInfo{}, fmt.Errorf("%w: pull_request payload missing action", ErrMalformedPayload)
	}
	info := PullRequestInfo{Action: p.Action}
	if !prRunActions[p.Action] {
		return model.Event{}, info, ErrIgnoredPush
	}
	head := p.Pr.Head
	if head.SHA == "" || head.Repo.Name == "" || head.Repo.Owner.Login == "" || p.Pr.Base.Repo.Name == "" {
		return model.Event{}, PullRequestInfo{}, fmt.Errorf("%w: pull_request payload missing head/base fields", ErrMalformedPayload)
	}
	ev := model.Event{
		Provider:   "github",
		Name:       "pull_request",
		DeliveryID: deliveryID,
		Repository: model.RepositoryRef{Provider: "github", Owner: head.Repo.Owner.Login, Name: head.Repo.Name},
		Ref:        "refs/heads/" + head.Ref,
		SHA:        head.SHA,
		Actor:      head.Repo.Owner.Login,
	}
	info.Fork, info.BaseRef, info.HeadRef = head.Repo.Fork, p.Pr.Base.Ref, head.Ref
	return ev, info, nil
}

func isZeroSHA(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return len(s) > 0
}

// checkRunPayload carries the fields needed for rerequest handling.
type checkRunPayload struct {
	Action   string `json:"action"`
	CheckRun struct {
		ExternalID string `json:"external_id"`
		HeadSHA    string `json:"head_sha"`
	} `json:"check_run"`
}

// DecodeCheckRunRerequest extracts the JobRunID to rerequest from a
// check_run payload. Only action "rerequested" is accepted; others return
// ErrIgnoredPush.
func DecodeCheckRunRerequest(body []byte) (model.JobRunID, error) {
	var p checkRunPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "", fmt.Errorf("%w: %w", ErrMalformedPayload, err)
	}
	if p.Action != "rerequested" {
		return "", ErrIgnoredPush
	}
	if p.CheckRun.ExternalID == "" {
		return "", fmt.Errorf("%w: check_run missing external_id", ErrMalformedPayload)
	}
	return model.JobRunID(p.CheckRun.ExternalID), nil
}
