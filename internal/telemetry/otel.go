//go:build darwin

// Package telemetry wires the weave CLI to OpenTelemetry. It covers all three
// signals (traces, metrics, logs) and activates up to three exporters based
// solely on environment variables — OTLP (any OTEL_EXPORTER_OTLP_* var set),
// stdout (OTEL_{TRACES,METRICS,LOGS}_EXPORTER=stdout), and Sentry (SENTRY_DSN
// set). When no environment variables are present no-op providers are used and
// the process incurs negligible overhead.
//
// Typical usage in execute.go:
//
//	telemetry.Configure()
//	defer telemetry.OTelShared().Flush()
//	_, span := telemetry.OTelShared().Tracer.Start(ctx, "command")
package telemetry

import (
	"context"
	"sync"
	"time"

	sentry "github.com/getsentry/sentry-go"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/deploymenttheory/weave/internal/ci"
)

// OTel is the weave telemetry singleton. Access it via OTelShared().
type OTel struct {
	// Tracer is the named tracer for weave command-level spans.
	Tracer trace.Tracer
	// Meter is the named meter for weave metric instruments.
	Meter metric.Meter
	// Instruments holds all named metric instruments for weave subsystems.
	Instruments Instruments

	providers *providerBundle
}

// options holds optional overrides applied by Configure() before OTelShared()
// first constructs the singleton.
type options struct {
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
}

// Option configures the OTel singleton before it is constructed.
type Option func(*options)

// WithTracerProvider injects a pre-built TracerProvider — useful in tests.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) { o.tracerProvider = tp }
}

// WithMeterProvider injects a pre-built MeterProvider — useful in tests.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *options) { o.meterProvider = mp }
}

var (
	configuredOpts []Option
	configMu       sync.Mutex
)

// Configure applies options and builds the OTel providers before OTelShared()
// is first called. It is safe to call multiple times; subsequent calls are
// ignored after OTelShared() has already been invoked. Call it from execute.go
// before startCommandSpan().
func Configure(opts ...Option) {
	configMu.Lock()
	defer configMu.Unlock()
	configuredOpts = append(configuredOpts, opts...)
}

// otelOnce guards singleton construction.
var otelOnce sync.Once
var otelInstance *OTel

// OTelShared returns the weave OTel singleton. On first call it reads all
// pending Configure() options and, when no provider override is present,
// auto-constructs providers from OTEL_* and SENTRY_DSN environment variables.
// When no environment variables are set the OTel global no-op providers remain
// in effect and the call overhead is negligible.
func OTelShared() *OTel {
	otelOnce.Do(func() {
		configMu.Lock()
		opts := configuredOpts
		configMu.Unlock()

		o := &options{}
		for _, opt := range opts {
			opt(o)
		}

		instance := &OTel{}

		// If providers are injected (test or advanced use) register them and
		// use them directly without building exporters.
		if o.tracerProvider != nil {
			otel.SetTracerProvider(o.tracerProvider)
		}
		if o.meterProvider != nil {
			otel.SetMeterProvider(o.meterProvider)
		}

		// When neither provider is injected, build from env vars.
		if o.tracerProvider == nil && o.meterProvider == nil {
			ctx := resourceContext()
			res := mustBuildResource()
			exporters, err := buildExporters(ctx)
			if err == nil {
				bundle, berr := buildProviders(res, exporters)
				if berr == nil {
					instance.providers = bundle
				}
			}
		}

		instance.Tracer = otel.Tracer("weave",
			trace.WithInstrumentationVersion(ci.CIVersion()))
		instance.Meter = otel.Meter("weave",
			metric.WithInstrumentationVersion(ci.CIVersion()))
		instance.Instruments = buildInstruments(instance.Meter)

		// Dual-emit: route logging.LogInfo/LogError to OTel log records so
		// they appear alongside spans in trace backends.
		WireLogBridge()

		otelInstance = instance
	})
	return otelInstance
}

// Flush blocks until all buffered spans, metrics, and log records have been
// delivered to their exporters, or until 5 seconds elapses. It also flushes
// the Sentry SDK when it has been initialised. Call it at every process exit
// site in execute.go.
func (o *OTel) Flush() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if o.providers != nil {
		_ = o.providers.shutdown(ctx)
	}

	if sentryActive {
		sentry.Flush(2 * time.Second)
	}
}
