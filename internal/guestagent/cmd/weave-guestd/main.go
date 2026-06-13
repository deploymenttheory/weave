// Command weave-guestd is the weave guest agent. The host engine deploys it
// into the guest and drives it over an SSH stdio channel. It multiplexes
// feature modules (clipboard today; more to come) using the framing in
// guestagent/proto and the dispatch loop in guestagent/agent.
package main

import (
	"os"

	"github.com/deploymenttheory/weave/internal/guestagent/agent"
	clipguest "github.com/deploymenttheory/weave/internal/guestagent/modules/clipboard"
	"github.com/deploymenttheory/weave/internal/guestagent/proto"
)

func main() {
	registry := agent.NewRegistry(
		clipguest.New(),
		// Future modules register here (file transfer, exec, telemetry, …).
	)

	in := proto.NewBufferedReader(os.Stdin)
	out := proto.NewBufferedWriter(os.Stdout)
	if err := agent.Serve(in, out, registry); err != nil {
		os.Stderr.WriteString("weave-guestd: " + err.Error() + "\n")
		os.Exit(1)
	}
}
