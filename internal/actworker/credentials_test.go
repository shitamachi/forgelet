package actworker

import (
	"testing"

	"github.com/nektos/act/pkg/model"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestExplicitPackageCredentialDoesNotReplaceImplicitCheckout(t *testing.T) {
	checkout := &model.Step{Uses: "actions/checkout@fixed", With: map[string]string{"persist-credentials": "false"}}
	explicit := &model.Step{Uses: "actions/checkout@fixed", With: map[string]string{"token": "${{ secrets.SDK }}"}}
	wf := &model.Workflow{Jobs: map[string]*model.Job{"job": {Steps: []*model.Step{checkout, explicit}}}}
	original := map[string]string{"SDK": "dependency-read-token"}
	secrets := executionSecrets(wf, run.ActRequest{Secrets: original, Token: "source-read-token", PackageToken: "package-write-token"})
	if checkout.With["token"] != "source-read-token" || explicit.With["token"] != "${{ secrets.SDK }}" || secrets["GITHUB_TOKEN"] != "package-write-token" || original["GITHUB_TOKEN"] != "" {
		t.Fatal("credential capabilities mixed", secrets, checkout.With, explicit.With)
	}
}
