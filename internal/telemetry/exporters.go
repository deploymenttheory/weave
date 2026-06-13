//go:build darwin

package telemetry

import (
	"context"
	"os"

	sentry "github.com/getsentry/sentry-go"
	sentryotel "github.com/getsentry/sentry-go/otel"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/deploymenttheory/weave/internal/ci"
)

// sentryActive is set to true after a successful sentry.Init so that Flush
// can call sentry.Flush before the process exits.
var sentryActive bool

// exporterSet holds all active exporters derived from environment variables.
// Each field may be nil/empty when the corresponding exporter is not configured.
type exporterSet struct {
	// traceExporters are wrapped in BatchSpanProcessors.
	traceExporters []sdktrace.SpanExporter
	// metricReaders are added to the MeterProvider.
	metricReaders []sdkmetric.Reader
	// logExporters are wrapped in BatchProcessors.
	logExporters []sdklog.Exporter
}

// buildExporters constructs the active exporter set from environment variables.
// It does not error — unconfigured exporters are silently omitted. Callers
// receive an empty set (all no-op providers) when no environment variables
// are present, which is the correct silent-dev behaviour.
func buildExporters(ctx context.Context) (*exporterSet, error) {
	set := &exporterSet{}

	if err := set.maybeAddOTLP(ctx); err != nil {
		return nil, err
	}
	if err := set.maybeAddStdout(); err != nil {
		return nil, err
	}
	set.maybeAddSentry()

	return set, nil
}

// maybeAddOTLP adds OTLP HTTP exporters for all three signals when
// OTEL_EXPORTER_OTLP_ENDPOINT or a signal-specific endpoint is set.
// The OTLP HTTP exporters read endpoint/header/timeout/compression config
// from the standard OTEL_EXPORTER_OTLP_* environment variables automatically.
func (e *exporterSet) maybeAddOTLP(ctx context.Context) error {
	if !otlpEndpointSet() {
		return nil
	}

	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return err
	}
	e.traceExporters = append(e.traceExporters, traceExp)

	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return err
	}
	e.metricReaders = append(e.metricReaders, sdkmetric.NewPeriodicReader(metricExp))

	logExp, err := otlploghttp.New(ctx)
	if err != nil {
		return err
	}
	e.logExporters = append(e.logExporters, logExp)

	return nil
}

// maybeAddStdout adds stdout exporters for each signal independently, keyed
// by the standard OTEL_{TRACES,METRICS,LOGS}_EXPORTER=stdout env vars.
func (e *exporterSet) maybeAddStdout() error {
	if os.Getenv("OTEL_TRACES_EXPORTER") == "stdout" {
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return err
		}
		e.traceExporters = append(e.traceExporters, exp)
	}

	if os.Getenv("OTEL_METRICS_EXPORTER") == "stdout" {
		exp, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
		if err != nil {
			return err
		}
		e.metricReaders = append(e.metricReaders, sdkmetric.NewPeriodicReader(exp))
	}

	if os.Getenv("OTEL_LOGS_EXPORTER") == "stdout" {
		exp, err := stdoutlog.New(stdoutlog.WithPrettyPrint())
		if err != nil {
			return err
		}
		e.logExporters = append(e.logExporters, exp)
	}

	return nil
}

// maybeAddSentry initialises the Sentry SDK when SENTRY_DSN is set.
// It registers sentryotel.NewOtelIntegration so that Sentry errors captured
// via sentry.CaptureException are automatically linked to the active OTel
// trace. SENTRY_ENVIRONMENT and SENTRY_RELEASE override the defaults.
//
// To forward OTel trace data to Sentry, set OTEL_EXPORTER_OTLP_ENDPOINT to
// your project's Sentry OTLP ingest URL alongside SENTRY_DSN; the OTLP
// exporter (activated by maybeAddOTLP) sends the spans while Sentry Init
// handles error capture and trace linking.
func (e *exporterSet) maybeAddSentry() {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return
	}

	release := os.Getenv("SENTRY_RELEASE")
	if release == "" {
		release = ci.CIRelease()
	}
	environment := os.Getenv("SENTRY_ENVIRONMENT")
	if environment == "" {
		environment = "production"
	}

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:         dsn,
		Release:     release,
		Environment: environment,
		Integrations: func(integrations []sentry.Integration) []sentry.Integration {
			return append(integrations, sentryotel.NewOtelIntegration())
		},
	}); err != nil {
		return
	}

	sentryActive = true
}

// otlpEndpointSet reports whether any OTLP endpoint env var is configured.
func otlpEndpointSet() bool {
	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}
