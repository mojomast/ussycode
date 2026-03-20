package telemetry

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	setupOnce sync.Once
	tracer    = otel.Tracer("github.com/mojomast/ussycode")
	meter     = otel.Meter("github.com/mojomast/ussycode")

	execCounter          metric.Int64Counter
	execLatency          metric.Float64Histogram
	browserTokenCounter  metric.Int64Counter
	smtpDeliveryCounter  metric.Int64Counter
	proxyDecisionCounter metric.Int64Counter
	telemetrySetupErr    error
	telemetrySetupDone   bool
)

func Setup(ctx context.Context, serviceName, serviceVersion string, logger *slog.Logger) (func(context.Context) error, error) {
	shutdown := func(context.Context) error { return nil }
	setupOnce.Do(func() {
		endpoint := strings.TrimSpace(os.Getenv("USSYCODE_OTLP_ENDPOINT"))
		if endpoint == "" {
			endpoint = "http://localhost:3474"
		}

		host, insecure, err := parseEndpoint(endpoint)
		if err != nil {
			telemetrySetupErr = err
			return
		}

		res, err := resource.New(ctx,
			resource.WithAttributes(
				semconv.ServiceName(serviceName),
				semconv.ServiceVersion(serviceVersion),
			),
		)
		if err != nil {
			telemetrySetupErr = err
			return
		}

		traceOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(host)}
		if insecure {
			traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		}
		traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
		if err != nil {
			telemetrySetupErr = err
			return
		}

		metricOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(host)}
		if insecure {
			metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
		}
		metricExporter, err := otlpmetrichttp.New(ctx, metricOpts...)
		if err != nil {
			telemetrySetupErr = err
			return
		}

		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
		)
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(10*time.Second))),
			sdkmetric.WithResource(res),
		)

		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		tracer = otel.Tracer(serviceName)
		meter = otel.Meter(serviceName)

		execCounter, _ = meter.Int64Counter("ussycode.api.exec.requests")
		execLatency, _ = meter.Float64Histogram("ussycode.api.exec.latency_ms")
		browserTokenCounter, _ = meter.Int64Counter("ussycode.browser.magic_tokens")
		smtpDeliveryCounter, _ = meter.Int64Counter("ussycode.smtp.deliveries")
		proxyDecisionCounter, _ = meter.Int64Counter("ussycode.proxy.auth.decisions")

		shutdown = func(ctx context.Context) error {
			var firstErr error
			if err := mp.Shutdown(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
			if err := tp.Shutdown(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
			return firstErr
		}

		telemetrySetupDone = true
		if logger != nil {
			logger.Info("telemetry configured", "endpoint", endpoint, "insecure", insecure)
		}
	})
	return shutdown, telemetrySetupErr
}

func parseEndpoint(endpoint string) (host string, insecure bool, err error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", false, err
	}
	if u.Host != "" {
		return u.Host, u.Scheme != "https", nil
	}
	return endpoint, true, nil
}

func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

func RecordExec(ctx context.Context, command, result string, duration time.Duration) {
	if !telemetrySetupDone {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("command", command),
		attribute.String("result", result),
	}
	execCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	execLatency.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(attrs...))
}

func RecordBrowserToken(ctx context.Context, action string) {
	if !telemetrySetupDone {
		return
	}
	browserTokenCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("action", action)))
}

func RecordSMTPDelivery(ctx context.Context, result string) {
	if !telemetrySetupDone {
		return
	}
	smtpDeliveryCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

func RecordProxyDecision(ctx context.Context, decision string) {
	if !telemetrySetupDone {
		return
	}
	proxyDecisionCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("decision", decision)))
}

func Enabled() bool {
	return telemetrySetupDone
}
