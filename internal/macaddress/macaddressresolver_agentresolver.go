// Port of tart's MACAddressResolver/AgentResolver.swift: asks the
// tart-guest-agent for the VM's IP over gRPC through the VM's control
// socket. The client stubs in example/weave/agentrpc are generated from the
// tart-guest-agent's proto/rpc/agent.proto.
//go:build darwin

package macaddress

import (
	"context"
	"net"
	"net/netip"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/deploymenttheory/weave/internal/agentrpc"
)

// AgentResolverResolveIP ports AgentResolver.ResolveIP(_:). Connection
// failures (e.g. no agent running in the guest) report "not found" so the
// caller can fall back to the DHCP and ARP strategies, mirroring the Swift
// original's GRPCConnectionPoolError handling.
func AgentResolverResolveIP(controlSocketPath string) (netip.Addr, bool, error) {
	// Create a gRPC channel connected to the VM's control socket.
	conn, err := grpc.NewClient("unix://"+controlSocketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", controlSocketPath)
		}))
	if err != nil {
		return netip.Addr{}, false, nil
	}
	defer conn.Close()

	// Invoke the ResolveIP() gRPC method with a one-second time limit.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	response, err := agentrpc.NewAgentClient(conn).ResolveIP(ctx, &agentrpc.ResolveIPRequest{})
	if err != nil {
		return netip.Addr{}, false, nil
	}

	ip, err := netip.ParseAddr(response.GetIp())
	if err != nil || !ip.Is4() {
		return netip.Addr{}, false, nil
	}
	return ip, true, nil
}
