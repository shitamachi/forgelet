package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shitamachi/forgelet/internal/run"
)

// A single pending event is retained unchanged if its response is lost. Final
// flushing uses the same bounded cancellation-independent context as log ACKs.
type progressReporter struct {
	mu          sync.Mutex
	client      *remoteClient
	seq         int64
	pending     *run.ProgressEvent
	annotations int
}

func (p *progressReporter) emit(ctx context.Context, e run.ProgressEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.flushPending(ctx); err != nil {
		return err
	}
	if e.Kind == "annotation" {
		if p.annotations >= 64 {
			return nil
		}
		if p.annotations == 63 {
			e.Level = "warning"
			e.Message = "警告显示数量已达上限，后续内容请查看完整日志。"
		}
	}
	if p.seq >= run.MaxProgressEvents {
		return fmt.Errorf("progress event limit exceeded")
	}
	e.Seq = p.seq + 1
	e.At = time.Now().UTC()
	p.pending = &e
	return p.flushPending(ctx)
}
func (p *progressReporter) flush(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flushPending(ctx)
}
func (p *progressReporter) flushPending(ctx context.Context) error {
	if p.pending == nil {
		return nil
	}
	var ack struct {
		Seq int64 `json:"seq"`
	}
	if err := p.client.request(ctx, http.MethodPost, "/progress", *p.pending, &ack); err != nil {
		return err
	}
	if ack.Seq != p.pending.Seq {
		return fmt.Errorf("progress acknowledgement mismatch")
	}
	if p.pending.Kind == "annotation" {
		p.annotations++
	}
	p.seq = ack.Seq
	p.pending = nil
	return nil
}

func reportProgress(ctx context.Context, opts Options, e run.ProgressEvent) error {
	if opts.Progress == nil {
		return nil
	}
	return opts.Progress(ctx, e)
}

func redactAnnotation(message string, mask *masker) string {
	for _, value := range mask.values {
		message = strings.ReplaceAll(message, value, "***")
	}
	if len(message) > 1024 {
		message = message[:1024]
	}
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func attachAnnotations(ctx context.Context, opts Options, sink *logSink) {
	if opts.Progress == nil {
		return
	}
	sink.annotation = func(level, message string) error {
		return reportProgress(ctx, opts, run.ProgressEvent{Kind: "annotation", StepID: sink.stepID, Level: level, Message: message})
	}
}
