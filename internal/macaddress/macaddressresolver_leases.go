// Port of tart's MACAddressResolver/Leases.swift: the /var/db/dhcpd_leases
// parser, modelled on PLCache_read() from Apple's bootp sources.
//go:build darwin

package macaddress

import (
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"
)

// LeasesError ports the LeasesError enum.
type LeasesError struct {
	Message string
}

func (e *LeasesError) Error() string { return e.Message }

func leasesUnexpectedFormat(message string, line int) *LeasesError {
	return &LeasesError{Message: fmt.Sprintf("unexpected DHCPD leases file format on line %d: %s", line, message)}
}

var errLeasesTruncated = &LeasesError{Message: "truncated DHCPD leases file"}

// Leases ports tart's Leases class.
type Leases struct {
	leases map[MACAddress]Lease
}

// NewLeases ports Leases.init?(): reads /var/db/dhcpd_leases. A missing file
// yields (nil, nil), like the Swift failable initializer.
func NewLeases() (*Leases, error) {
	return NewLeasesFromFile("/var/db/dhcpd_leases")
}

// NewLeasesFromFile ports Leases.init?(_ fromURL:).
func NewLeasesFromFile(path string) (*Leases, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return NewLeasesFromString(string(contents))
}

// NewLeasesFromString ports Leases.init(_ fromString:). When duplicate
// leases are found, the newer lease wins.
func NewLeasesFromString(contents string) (*Leases, error) {
	rawLeases, err := retrieveRawLeases(contents)
	if err != nil {
		return nil, err
	}

	leases := map[MACAddress]Lease{}
	for _, rawLease := range rawLeases {
		lease, ok := NewLease(rawLease)
		if !ok || !lease.ExpiresAt.After(time.Now()) {
			continue
		}
		if existing, ok := leases[lease.MAC]; !ok || lease.ExpiresAt.After(existing.ExpiresAt) {
			leases[lease.MAC] = lease
		}
	}

	return &Leases{leases: leases}, nil
}

// retrieveRawLeases ports Leases.retrieveRawLeases(_:).
func retrieveRawLeases(dhcpdLeasesContents string) ([]map[string]string, error) {
	var rawLeases []map[string]string

	const (
		stateNowhere = iota
		stateStart
		stateBody
		stateEnd
	)
	state := stateNowhere

	currentRawLease := map[string]string{}

	lineNumber := 0
	for line := range strings.SplitSeq(dhcpdLeasesContents, "\n") {
		lineNumber++

		switch {
		case line == "{":
			// Handle lease block start.
			if state != stateNowhere && state != stateEnd {
				return nil, leasesUnexpectedFormat("unexpected lease block start ({)", lineNumber)
			}
			state = stateStart
		case line == "}":
			// Handle lease block end.
			if state != stateBody {
				return nil, leasesUnexpectedFormat("unexpected lease block end (})", lineNumber)
			}
			rawLeases = append(rawLeases, currentRawLease)
			currentRawLease = map[string]string{}
			state = stateEnd
		default:
			// Handle lease block contents.
			lineWithoutTabs := strings.TrimLeft(line, " \t")
			if lineWithoutTabs == "" {
				continue
			}

			key, value, found := strings.Cut(lineWithoutTabs, "=")
			if !found {
				return nil, leasesUnexpectedFormat("key-value pair with only a key", lineNumber)
			}
			currentRawLease[key] = value
			state = stateBody
		}
	}

	if state == stateStart || state == stateBody {
		return nil, errLeasesTruncated
	}

	return rawLeases, nil
}

// ResolveMACAddress ports Leases.ResolveMACAddress(macAddress:).
func (l *Leases) ResolveMACAddress(macAddress MACAddress) (netip.Addr, bool) {
	lease, ok := l.leases[macAddress]
	return lease.IP, ok
}
