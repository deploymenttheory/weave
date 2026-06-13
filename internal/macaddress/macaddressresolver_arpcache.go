// Port of tart's MACAddressResolver/ARPCache.swift: resolves MAC→IP from
// the output of arp(8).
//go:build darwin

package macaddress

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// ARPCacheError covers the three error structs from ARPCache.swift.
type ARPCacheError struct {
	Message string
}

func (e *ARPCacheError) Error() string { return e.Message }

// ARPCache ports tart's ARPCache struct.
type ARPCache struct {
	arpCommandOutput []byte
}

// NewARPCache ports ARPCache.init(): runs "arp -an" and captures its output.
func NewARPCache() (*ARPCache, error) {
	task := foundation.NSTaskFromID(purego.Send[purego.ID](purego.ID(purego.GetClass("NSTask")), purego.RegisterName("new")))
	task.SetExecutableURL(objcutil.NSURLFromPath("/usr/sbin/arp"))
	task.SetArguments(objcutil.NSStringArray([]string{"-an"}))

	pipe := foundation.NSPipePipe()
	task.SetStandardOutput(pipe.Ptr())
	task.SetStandardError(pipe.Ptr())
	task.SetStandardInput(foundation.NSFileHandleFileHandleWithNullDevice().Ptr())

	if _, err := task.LaunchAndReturnError(); err != nil {
		return nil, err
	}

	outputNSData, err := pipe.FileHandleForReading().ReadDataToEndOfFileAndReturnError()
	if err != nil {
		return nil, err
	}
	output := objcutil.NSDataToBytes(outputNSData)
	if len(output) == 0 {
		return nil, &ARPCacheError{Message: "arp command yielded invalid output: empty output"}
	}

	task.WaitUntilExit()

	if !(task.TerminationReason() == foundation.NSTaskTerminationReasonExit && task.TerminationStatus() == 0) {
		reason := "uncaught signal"
		if task.TerminationReason() == foundation.NSTaskTerminationReasonExit {
			reason = fmt.Sprintf("exit code %d", task.TerminationStatus())
		}
		return nil, &ARPCacheError{Message: "arp command failed: " + reason}
	}

	return &ARPCache{arpCommandOutput: output}, nil
}

// arpLineRegex is based on arp.c from Apple's network_cmds.
var arpLineRegex = regexp.MustCompile(`^.* \((?P<ip>.*)\) at (?P<mac>.*) on (?P<interface>.*) .*$`)

// ResolveMACAddress ports ARPCache.ResolveMACAddress(macAddress:).
func (c *ARPCache) ResolveMACAddress(macAddress MACAddress) (netip.Addr, bool, error) {
	lines := strings.Split(strings.TrimSpace(string(c.arpCommandOutput)), "\n")

	for _, line := range lines {
		match := arpLineRegex.FindStringSubmatch(line)
		if match == nil {
			return netip.Addr{}, false, &ARPCacheError{
				Message: fmt.Sprintf("arp command yielded invalid output: unparseable entry %q", line)}
		}

		rawIP := match[arpLineRegex.SubexpIndex("ip")]
		ip, err := netip.ParseAddr(rawIP)
		if err != nil || !ip.Is4() {
			return netip.Addr{}, false, &ARPCacheError{
				Message: fmt.Sprintf("arp command yielded invalid output: failed to parse IPv4 address %s", rawIP)}
		}

		rawMAC := match[arpLineRegex.SubexpIndex("mac")]
		if rawMAC == "(incomplete)" {
			continue
		}
		mac, ok := NewMACAddress(rawMAC)
		if !ok {
			return netip.Addr{}, false, &ARPCacheError{
				Message: fmt.Sprintf("arp command yielded invalid output: failed to parse MAC address %s", rawMAC)}
		}

		if macAddress == mac {
			return ip, true, nil
		}
	}

	return netip.Addr{}, false, nil
}
