//go:build darwin

package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	gotellog "go.opentelemetry.io/otel/log/global"
)

// providerBundle holds the three constructed SDK providers. All fields are
// non-nil when active; the OTel globals are set to matching no-op providers
// when the bundle carries nils (empty exporter set).
type providerBundle struct {
	tracer *sdktrace.TracerProvider
	meter  *sdkmetric.MeterProvider
	logger *sdklog.LoggerProvider
}

// buildProviders constructs and globally registers SDK providers for all three
// signals from res and the active exporter set. When the exporter set is empty
// (no env vars configured) the OTel global no-op providers remain in effect.
func buildProviders(res *sdkresource.Resource, exporters *exporterSet) (*providerBundle, error) {
	bundle := &providerBundle{}

	tp, err := buildTracerProvider(res, exporters)
	if err != nil {
		return nil, err
	}
	bundle.tracer = tp

	mp, err := buildMeterProvider(res, exporters)
	if err != nil {
		return nil, err
	}
	bundle.meter = mp

	lp, err := buildLoggerProvider(res, exporters)
	if err != nil {
		return nil, err
	}
	bundle.logger = lp

	// Register composite text-map propagator so that W3C trace context and
	// baggage headers are propagated across HTTP boundaries automatically.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return bundle, nil
}

// shutdown gracefully flushes and shuts down all three providers within ctx.
func (b *providerBundle) shutdown(ctx context.Context) error {
	var errs []error
	if b.tracer != nil {
		if err := b.tracer.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if b.meter != nil {
		if err := b.meter.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if b.logger != nil {
		if err := b.logger.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func buildTracerProvider(res *sdkresource.Resource, exporters *exporterSet) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	}
	for _, exp := range exporters.traceExporters {
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	return tp, nil
}

func buildMeterProvider(res *sdkresource.Resource, exporters *exporterSet) (*sdkmetric.MeterProvider, error) {
	opts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
	}
	for _, reader := range exporters.metricReaders {
		opts = append(opts, sdkmetric.WithReader(reader))
	}
	mp := sdkmetric.NewMeterProvider(opts...)
	otel.SetMeterProvider(mp)
	return mp, nil
}

func buildLoggerProvider(res *sdkresource.Resource, exporters *exporterSet) (*sdklog.LoggerProvider, error) {
	opts := []sdklog.LoggerProviderOption{
		sdklog.WithResource(res),
	}
	for _, exp := range exporters.logExporters {
		opts = append(opts, sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)))
	}
	lp := sdklog.NewLoggerProvider(opts...)
	gotellog.SetLoggerProvider(lp)
	return lp, nil
}
