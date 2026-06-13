// Port of tart's OCI/RemoteName.swift. The ANTLR-generated Reference parser
// (Reference.g4) is replaced by a hand-written validator implementing the
// same grammar:
//
//	root:                host (':' port)? '/' namespace reference? EOF
//	host:                host_component ('.' host_component)*
//	host_component:      name ('-' name)*
//	port:                DIGIT+
//	namespace:           namespace_component ('/' namespace_component)*
//	namespace_component: (name separator?)+
//	reference:           (':' tag) | ('@' name ':' name)
//	tag:                 name (separator name)*
//	separator:           '.' | '-' | '_'
//	name:                (LETTER | DIGIT)+
//go:build darwin

package oci

import (
	"fmt"
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

// ReferenceType mirrors Reference.ReferenceType; Tag sorts before Digest.
type ReferenceType int

const (
	ReferenceTypeTag ReferenceType = iota
	ReferenceTypeDigest
)

// Reference mirrors tart's Reference struct.
type Reference struct {
	Type  ReferenceType
	Value string
}

func NewTagReference(tag string) Reference {
	return Reference{Type: ReferenceTypeTag, Value: tag}
}

func NewDigestReference(digest string) Reference {
	return Reference{Type: ReferenceTypeDigest, Value: digest}
}

// FullyQualified mirrors Reference.fullyQualified.
func (r Reference) FullyQualified() string {
	if r.Type == ReferenceTypeDigest {
		return "@" + r.Value
	}
	return ":" + r.Value
}

func (r Reference) String() string { return r.FullyQualified() }

// Less mirrors Reference's Comparable conformance.
func (r Reference) Less(other Reference) bool {
	if r.Type != other.Type {
		return r.Type < other.Type
	}
	return r.Value < other.Value
}

// RemoteName mirrors tart's RemoteName struct.
type RemoteName struct {
	Host      string
	Namespace string
	Reference Reference
}

func (n RemoteName) String() string {
	return n.Host + "/" + n.Namespace + n.Reference.FullyQualified()
}

// Less mirrors RemoteName's Comparable conformance.
func (n RemoteName) Less(other RemoteName) bool {
	if n.Host != other.Host {
		return n.Host < other.Host
	}
	if n.Namespace != other.Namespace {
		return n.Namespace < other.Namespace
	}
	return n.Reference.Less(other.Reference)
}

// NewRemoteName ports RemoteName.init(_:).
func NewRemoteName(name string) (RemoteName, error) {
	fail := func(why string) (RemoteName, error) {
		return RemoteName{}, weaveerrors.ErrFailedToParseRemoteName(why)
	}

	slash := strings.IndexByte(name, '/')
	if slash < 0 {
		return fail("expected a \"/\" separating the host from the namespace")
	}

	authority := name[:slash]
	rest := name[slash+1:]

	host, port, hasPort := strings.Cut(authority, ":")
	if !isReferenceHost(host) {
		return fail(fmt.Sprintf("invalid host %q", host))
	}
	if hasPort {
		if port == "" || !isReferenceDigits(port) {
			return fail(fmt.Sprintf("invalid port %q", port))
		}
		host += ":" + port
	}

	// Split off the reference: a digest ("@name:name") wins over a tag
	// (":tag"); neither character may legally appear inside a namespace.
	namespace := rest
	reference := ""
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		namespace, reference = rest[:at], rest[at:]
	} else if colon := strings.IndexByte(rest, ':'); colon >= 0 {
		namespace, reference = rest[:colon], rest[colon:]
	}

	if !isReferenceNamespace(namespace) {
		return fail(fmt.Sprintf("invalid namespace %q", namespace))
	}

	result := RemoteName{Host: host, Namespace: namespace}

	switch {
	case reference == "":
		result.Reference = NewTagReference("latest")
	case strings.HasPrefix(reference, "@"):
		digest := reference[1:]
		algorithm, hexDigest, ok := strings.Cut(digest, ":")
		if !ok || !isReferenceName(algorithm) || !isReferenceName(hexDigest) {
			return fail(fmt.Sprintf("invalid digest %q", digest))
		}
		if !strings.HasPrefix(reference, "@sha256:") {
			return fail("unknown reference format")
		}
		result.Reference = NewDigestReference(digest)
	default: // ":" tag
		tag := reference[1:]
		if !isReferenceTag(tag) {
			return fail(fmt.Sprintf("invalid tag %q", tag))
		}
		result.Reference = NewTagReference(tag)
	}

	return result, nil
}

func isReferenceNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isReferenceSeparator(c byte) bool {
	return c == '.' || c == '-' || c == '_'
}

// isReferenceName matches the grammar's name: (LETTER | DIGIT)+.
func isReferenceName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isReferenceNameByte(s[i]) {
			return false
		}
	}
	return true
}

func isReferenceDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// isReferenceHost matches host: host_component ('.' host_component)*, with
// host_component: name ('-' name)*.
func isReferenceHost(s string) bool {
	if s == "" {
		return false
	}
	for component := range strings.SplitSeq(s, ".") {
		for namePart := range strings.SplitSeq(component, "-") {
			if !isReferenceName(namePart) {
				return false
			}
		}
	}
	return true
}

// isReferenceTag matches tag: name (separator name)*.
func isReferenceTag(s string) bool {
	if s == "" || !isReferenceNameByte(s[0]) || !isReferenceNameByte(s[len(s)-1]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isReferenceNameByte(s[i]) && !isReferenceSeparator(s[i]) {
			return false
		}
		if i > 0 && isReferenceSeparator(s[i]) && isReferenceSeparator(s[i-1]) {
			return false
		}
	}
	return true
}

// isReferenceNamespace matches namespace: namespace_component ('/'
// namespace_component)*, with namespace_component: (name separator?)+ —
// i.e. each component starts with an alphanumeric run and may interleave
// (and end with) single separators.
func isReferenceNamespace(s string) bool {
	if s == "" {
		return false
	}
	for component := range strings.SplitSeq(s, "/") {
		if component == "" || !isReferenceNameByte(component[0]) {
			return false
		}
		for i := 0; i < len(component); i++ {
			if !isReferenceNameByte(component[i]) && !isReferenceSeparator(component[i]) {
				return false
			}
			if i > 0 && isReferenceSeparator(component[i]) && isReferenceSeparator(component[i-1]) {
				return false
			}
		}
	}
	return true
}
