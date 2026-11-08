package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/shitamachi/forgelet/internal/run"
)

const imageVerificationError = "external image verification: "

// A successful result cannot unblock delivery until the registry confirms the
// immutable reference and its single-platform configuration.
func verifyExternalImage(ctx context.Context, build run.ExternalBuild, digest string) (string, error) {
	if !run.ValidDigest(digest) {
		return "build did not publish a valid digest", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	nameOptions := []name.Option{name.StrictValidation}
	if build.InsecureRegistry {
		nameOptions = append(nameOptions, name.Insecure)
	}
	ref, err := name.NewDigest(build.Image+"@"+digest, nameOptions...)
	if err != nil {
		return "invalid image repository", nil
	}
	options := []remote.Option{remote.WithContext(ctx), remote.WithTransport(imageVerificationTransport{allowHTTP: build.InsecureRegistry})}
	if build.InsecureRegistry {
		options = append(options, remote.WithAuth(authn.Anonymous))
	} else {
		options = append(options, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	}
	desc, err := remote.Get(ref, options...)
	if err != nil {
		return imageRegistryError(err)
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(desc.Manifest)) != digest {
		return "registry returned a different manifest digest", nil
	}
	if !desc.MediaType.IsImage() {
		return "registry did not return a single-platform image manifest", nil
	}
	img, err := desc.Image()
	if err != nil {
		return "registry returned an invalid image manifest", nil
	}
	config, err := img.ConfigFile()
	if err != nil {
		return imageRegistryError(err)
	}
	if actual := config.OS + "/" + config.Architecture; actual != build.Platform {
		return fmt.Sprintf("expected %s, registry image is %s", build.Platform, actual), nil
	}
	return "", nil
}

func imageRegistryError(err error) (string, error) {
	var response *transport.Error
	if errors.As(err, &response) {
		switch response.StatusCode {
		case http.StatusNotFound:
			return "image manifest or configuration is missing in the registry", nil
		case http.StatusUnauthorized, http.StatusForbidden:
			return "control plane has no read access to the registry image", nil
		}
	}
	return "", err
}

type imageVerificationTransport struct{ allowHTTP bool }

func (t imageVerificationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && !t.allowHTTP {
		return nil, errors.New("image verification requires registry TLS")
	}
	resp, err := remote.DefaultTransport.RoundTrip(req)
	if err == nil {
		resp.Body = http.MaxBytesReader(nil, resp.Body, 1<<20)
	}
	return resp, err
}

func (s *Server) verifyExternalResult(w http.ResponseWriter, r *http.Request, job run.Job, result *run.Result) bool {
	if job.Plan.ExternalBuild == nil || result.State != run.Succeeded || job.CancelRequested {
		return true
	}
	var problem string
	if job.State.Terminal() {
		// A replay after the response was lost must not depend on the registry
		// still being available. Finish still compares the complete result.
		if job.Result != nil {
			if detail, ok := strings.CutPrefix(job.Result.Error, imageVerificationError); ok {
				problem = detail
			}
		}
	} else {
		var err error
		problem, err = verifyExternalImage(r.Context(), *job.Plan.ExternalBuild, result.Outputs[run.ExternalImageDigestOutput])
		if err != nil {
			http.Error(w, "image registry verification unavailable", http.StatusServiceUnavailable)
			return false
		}
	}
	if problem != "" {
		result.State, result.Error, result.Outputs = run.Failed, imageVerificationError+problem, nil
	}
	return true
}
