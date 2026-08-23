package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSetupWithoutEndpointIsNoop(t *testing.T) {
	tp, shutdown, err := Setup(context.Background(), "", "forgelet-test")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, ok := tp.(*sdktrace.TracerProvider); ok {
		t.Error("empty endpoint must not produce an SDK provider")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

// Outgoing requests through Transport carry W3C trace context.
func TestTransportPropagatesTraceContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Traceparent")
	}))
	defer srv.Close()

	ctx, span := tp.Tracer("test").Start(context.Background(), "outbound")
	client := &http.Client{Transport: Transport(srv.Client().Transport, tp)}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()
	span.End()

	if got == "" {
		t.Fatal("Traceparent header missing on outgoing request")
	}
}

// The middleware extracts incoming trace context: the server span continues
// the caller's trace instead of starting a new one.
func TestMiddlewareExtractsIncomingContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}))
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	h := Middleware(tp, "forgelet-test", inner)

	ctx, parent := tp.Tracer("test").Start(context.Background(), "client-call")
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/webhooks/github", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	parent.End()

	spans := recorder.Ended()
	if len(spans) < 2 {
		t.Fatalf("spans = %d, want at least 2 (server + client)", len(spans))
	}
	var serverSpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "POST" || strings.Contains(s.Name(), "webhooks") {
			serverSpan = s
		}
	}
	if serverSpan == nil {
		t.Fatalf("no server span among %v", spans)
	}
	if sc := serverSpan.SpanContext(); !sc.IsValid() || sc.TraceID() != parent.SpanContext().TraceID() {
		t.Errorf("server span trace %s, want %s", sc.TraceID(), parent.SpanContext().TraceID())
	}
}
