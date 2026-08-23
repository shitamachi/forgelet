// Package tracing wires OpenTelemetry distributed tracing through the key
// control-plane paths (spec 0010 FR-O3). With an empty OTLP endpoint the
// provider is a no-op: spans are created but never exported.
package tracing

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Setup installs the W3C propagator globally and returns a tracer provider
// plus a shutdown hook. endpoint is host[:port] of an OTLP HTTP collector;
// empty disables exporting entirely (dev/test default).
func Setup(ctx context.Context, endpoint, serviceName string) (trace.TracerProvider, func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	if endpoint == "" {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure())
	if err != nil {
		return nil, nil, fmt.Errorf("tracing: otlp exporter: %w", err)
	}
	res, err := sdkresource.Merge(sdkresource.Default(),
		sdkresource.NewSchemaless(semconv.ServiceNameKey.String(serviceName)))
	if err != nil {
		return nil, nil, fmt.Errorf("tracing: resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp, tp.Shutdown, nil
}

// Middleware wraps an HTTP handler so every request starts a server span
// and incoming trace context (W3C traceparent) is extracted. Use it at the
// composition root so webhook, internal API and metrics share one path.
func Middleware(tp trace.TracerProvider, service string, next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, service, otelhttp.WithTracerProvider(tp))
}

// Transport wraps a round tripper so outgoing requests inject trace context.
// GitHub API adapters and the executor control-plane client use this to stay
// part of the same trace as the run they serve.
func Transport(base http.RoundTripper, tp trace.TracerProvider) http.RoundTripper {
	return otelhttp.NewTransport(base, otelhttp.WithTracerProvider(tp))
}
