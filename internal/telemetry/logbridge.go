//go:build darwin

package telemetry

import (
	"context"
	"log/slog"

	otelslog "go.opentelemetry.io/contrib/bridges/otelslog"

	"github.com/deploymenttheory/weave/internal/logging"
)

// WireLogBridge installs an OTel log sink on the weave logging package so
// that every logging.LogInfo / logging.LogError call dual-emits a structured
// OTel log record in addition to the existing file output. Records are
// correlated with the active trace via the global OTel logger provider, which
// must already be set (i.e. call this after OTelShared() has been invoked or
// after Configure() has registered a provider).
//
// WireLogBridge is called automatically by OTelShared() — callers do not need
// to call it explicitly.
func WireLogBridge() {
	logger := otelslog.NewLogger("weave")
	logging.SetOTelSink(func(severity, message string) {
		level := slog.LevelInfo
		if severity == "ERROR" {
			level = slog.LevelError
		}
		logger.Log(context.Background(), level, message)
	})
}
