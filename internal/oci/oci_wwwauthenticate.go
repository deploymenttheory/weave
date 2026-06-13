// Port of tart's OCI/WWWAuthenticate.swift: a WWW-Authenticate header
// parser per RFC 2617 §3.2.1 and RFC 6750 §3.
//go:build darwin

package oci

import "strings"

// WWWAuthenticate ports tart's WWWAuthenticate class.
type WWWAuthenticate struct {
	Scheme string
	KVs    map[string]string
}

// NewWWWAuthenticate ports WWWAuthenticate.init(rawHeaderValue:).
func NewWWWAuthenticate(rawHeaderValue string) (*WWWAuthenticate, error) {
	scheme, rawDirectives, ok := strings.Cut(rawHeaderValue, " ")
	if !ok {
		return nil, registryErrorMalformedHeader("WWW-Authenticate header should consist of two parts: scheme and directives")
	}

	result := &WWWAuthenticate{Scheme: scheme, KVs: map[string]string{}}

	for _, sequence := range contextAwareCommaSplit(rawDirectives) {
		key, value, ok := strings.Cut(sequence, "=")
		if !ok {
			return nil, registryErrorMalformedHeader("Each WWW-Authenticate header directive should be in the form of key=value or key=\"value\"")
		}
		result.KVs[key] = strings.Trim(value, "\"")
	}

	return result, nil
}

// contextAwareCommaSplit splits on commas that are outside quoted strings.
func contextAwareCommaSplit(rawDirectives string) []string {
	var result []string
	inQuotation := false
	var accumulator strings.Builder

	for _, ch := range rawDirectives {
		if ch == ',' && !inQuotation {
			result = append(result, accumulator.String())
			accumulator.Reset()
			continue
		}

		accumulator.WriteRune(ch)

		if ch == '"' {
			inQuotation = !inQuotation
		}
	}

	if accumulator.Len() > 0 {
		result = append(result, accumulator.String())
	}

	return result
}
