//go:build darwin

package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"github.com/deploymenttheory/weave/internal/ci"
	"github.com/deploymenttheory/weave/internal/platform"
)

// buildResource assembles the weave-centric OTel resource attached to every
// span, metric, and log record. Attributes are derived from the running host
// and the build version; no CI-specific attributes are included here — the
// caller's span enrichment (execute.go startCommandSpan) handles those.
func buildResource() (*resource.Resource, error) {
	hostArch := string(platform.CurrentArchitecture())
	osVersion := platform.DeviceInfoOS()

	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("weave"),
			semconv.ServiceVersion(ci.CIVersion()),
			semconv.OSTypeDarwin,
			semconv.OSVersion(osVersion),
			semconv.OSName(fmt.Sprintf("macOS (%s)", hostArch)),
			semconv.HostArchKey.String(hostArch),
			semconv.ProcessPID(os.Getpid()),
			semconv.ProcessExecutableName("weave"),
		),
	)
}

// mustBuildResource is buildResource but panics if the resource cannot be
// constructed — used in OTelShared where the context is a package-level init.
func mustBuildResource() *resource.Resource {
	res, err := buildResource()
	if err != nil {
		// resource.Merge only fails on schema-URL conflicts between the two
		// halves, which cannot happen with our well-formed inputs.
		panic(fmt.Sprintf("telemetry: failed to build OTel resource: %v", err))
	}
	return res
}

// resourceContext returns a context used only during provider construction
// (resource detection is synchronous here, but the SDK API requires one).
func resourceContext() context.Context { return context.Background() }
