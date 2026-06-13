//go:build darwin

package telemetry

import (
	"go.opentelemetry.io/otel/metric"
)

// Instruments holds the named metric instruments used across weave packages.
// Obtain them via OTelShared().Instruments after telemetry.Configure().
type Instruments struct {
	// CommandDuration records the wall-clock duration of each weave command in
	// milliseconds. Set the "command" attribute to the command name.
	CommandDuration metric.Int64Histogram
	// VMOperations counts VM lifecycle operations (create, start, stop, clone,
	// snapshot). Set the "operation" and "vm.name" attributes.
	VMOperations metric.Int64Counter
	// OCIBytesTransferred records the total bytes transferred per OCI
	// pull/push. Set the "direction" (pull/push) and "image" attributes.
	OCIBytesTransferred metric.Int64Histogram
	// SSHConnections counts SSH dial attempts. Set the "host" attribute.
	SSHConnections metric.Int64Counter
}

// buildInstruments creates all metric instruments from meter. It is called
// once during OTelShared() construction; errors are silently ignored so that a
// missing or no-op meter never prevents the process from starting.
func buildInstruments(meter metric.Meter) Instruments {
	commandDuration, _ := meter.Int64Histogram(
		"weave.command.duration_ms",
		metric.WithDescription("Wall-clock duration of a weave CLI command in milliseconds."),
		metric.WithUnit("ms"),
	)
	vmOps, _ := meter.Int64Counter(
		"weave.vm.operations",
		metric.WithDescription("Count of VM lifecycle operations (create, start, stop, clone, snapshot)."),
	)
	ociBytes, _ := meter.Int64Histogram(
		"weave.oci.bytes_transferred",
		metric.WithDescription("Total bytes transferred per OCI image pull or push."),
		metric.WithUnit("By"),
	)
	sshConns, _ := meter.Int64Counter(
		"weave.ssh.connections",
		metric.WithDescription("Count of SSH dial attempts."),
	)
	return Instruments{
		CommandDuration:     commandDuration,
		VMOperations:        vmOps,
		OCIBytesTransferred: ociBytes,
		SSHConnections:      sshConns,
	}
}
