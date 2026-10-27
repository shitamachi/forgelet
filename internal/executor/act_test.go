package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestActProtocolMasksDecodedSplitLogsAndDynamicSecrets(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mask := newMasker()
	if err := mask.add("secret-with-\"quote"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	p := &actProtocol{ctx: ctx, cancel: cancel, mask: mask, opts: Options{Log: func(step string, data []byte) error {
		if step != "one" {
			t.Fatal(step)
		}
		_, err := logs.Write(data)
		return err
	}}}
	for _, message := range []run.ActMessage{{StepID: "one", Message: "prefix secret-with-\"quote\n"}, {Mask: "dynamic-secret"}, {StepID: "one", Message: "dynamic-secret\n"}, {Result: &run.Result{State: run.Succeeded}}} {
		data, _ := json.Marshal(message)
		data = append(data, '\n')
		for _, b := range data {
			if _, err := p.Write([]byte{b}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "secret") || !strings.Contains(logs.String(), "***") {
		t.Fatal(logs.String())
	}
	if p.result == nil || p.result.State != run.Succeeded {
		t.Fatal(p.result)
	}
}
func TestActProtocolFailureCancelsAndDoesNotBecomeSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := &actProtocol{ctx: ctx, cancel: cancel, mask: newMasker(), opts: Options{Log: func(string, []byte) error { return errors.New("upload unavailable") }}}
	data, _ := json.Marshal(run.ActMessage{StepID: "one", Message: "log\n"})
	_, err := p.Write(append(data, '\n'))
	if err == nil || ctx.Err() == nil {
		t.Fatal("log failure did not stop execution")
	}
	p = &actProtocol{ctx: t.Context(), mask: newMasker()}
	if _, err := p.Write([]byte("unstructured private diagnostic\n")); err == nil {
		t.Fatal("accepted raw upstream output")
	}
}

func TestActCommandFailureRetainsRealExitCode(t *testing.T) {
	for _, reported := range []bool{false, true} {
		p := &actProtocol{ctx: t.Context(), mask: newMasker()}
		if reported {
			code := 7
			data, _ := json.Marshal(run.ActMessage{StepID: "bad", ExitCode: &code})
			if _, err := p.Write(append(data, '\n')); err != nil {
				t.Fatal(err)
			}
		}
		data, _ := json.Marshal(run.ActMessage{Result: &run.Result{State: run.Failed, Steps: []run.StepResult{{ID: "bad", State: run.Failed, Outcome: run.Failed, ExitCode: -1}}}})
		if _, err := p.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		if reported && (p.result.Steps[0].ExitCode != 7 || p.result.Error != "") {
			t.Fatal(p.result)
		}
		if !reported && p.result.Error == "" {
			t.Fatal("infrastructure failure became ordinary command failure")
		}
	}
}
