// Reference resolution: maps a user-supplied image reference plus an
// optional registry-profile override onto a transport client and a
// fully-qualified RemoteName.
//
// Resolution order:
//  1. --registry <name>: the named profile; the reference is repo[:tag]
//     relative to the profile's host/organization.
//  2. A fully-qualified reference (host/namespace[:tag|@digest], i.e.
//     oci.NewRemoteName succeeds) is used verbatim — historical behaviour.
//     A host-matching profile contributes its insecure default.
//  3. A bare name resolves against the default profile.
//
// Note that profile names never disambiguate inside the reference itself
// ("cua/macos-x:latest" parses as host "cua") — use --registry for that.
//go:build darwin

package registry

import (
	"strings"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/oci"
)

// Resolve maps imageReference to a transport client and a fully-qualified
// name. profileOverride is the --registry flag (empty: none); insecureFlag
// is the command's --insecure flag and always wins over profile defaults.
func Resolve(imageReference string, profileOverride string, insecureFlag bool,
	settings *weaveconfig.Settings) (Client, oci.RemoteName, error) {
	remoteName, insecure, err := ResolveName(imageReference, profileOverride, insecureFlag, settings)
	if err != nil {
		return nil, oci.RemoteName{}, err
	}
	client, err := NewOCIClient(remoteName.Host, remoteName.Namespace, insecure)
	if err != nil {
		return nil, oci.RemoteName{}, err
	}
	return client, remoteName, nil
}

// ResolveName applies the resolution rules without opening a transport,
// returning the fully-qualified name and the effective insecure flag.
func ResolveName(imageReference string, profileOverride string, insecureFlag bool,
	settings *weaveconfig.Settings) (oci.RemoteName, bool, error) {
	// 1. Explicit profile.
	if profileOverride != "" {
		profile, ok := settings.FindRegistryProfile(profileOverride)
		if !ok {
			return oci.RemoteName{}, false, weaveerrors.ErrGeneric(
				"unknown registry profile %q (run \"weave config registry list\")", profileOverride)
		}
		return resolveNameAgainstProfile(imageReference, profile, insecureFlag)
	}

	// 2. Fully-qualified reference.
	if remoteName, err := oci.NewRemoteName(imageReference); err == nil {
		insecure := insecureFlag
		if profile, ok := settings.RegistryProfileForHost(remoteName.Host); ok && profile.IsInsecure {
			insecure = true
		}
		return remoteName, insecure, nil
	}

	// 3. Bare name against the default profile.
	profile, ok := settings.DefaultRegistryProfile()
	if !ok {
		return oci.RemoteName{}, false, weaveerrors.ErrGeneric(
			"%q is not a fully-qualified image reference and no default registry profile is configured\n"+
				"(use host/namespace/name:tag, or configure one: weave config registry add <name> --host ghcr.io --organization <org> --default)",
			imageReference)
	}
	return resolveNameAgainstProfile(imageReference, profile, insecureFlag)
}

// resolveNameAgainstProfile prefixes a repo[:tag|@digest] reference with the
// profile's coordinates.
func resolveNameAgainstProfile(imageReference string, profile weaveconfig.RegistryProfile,
	insecureFlag bool) (oci.RemoteName, bool, error) {
	switch profile.Type {
	case "", "oci":
		// Fall through below.
	default:
		return oci.RemoteName{}, false, weaveerrors.ErrGeneric(
			"registry profile %q has unsupported type %q", profile.Name, profile.Type)
	}

	qualified := profile.Host + "/" + profile.Organization + "/" + strings.TrimPrefix(imageReference, "/")
	remoteName, err := oci.NewRemoteName(qualified)
	if err != nil {
		return oci.RemoteName{}, false, weaveerrors.ErrGeneric(
			"%q does not form a valid reference under registry profile %q (%s): %v",
			imageReference, profile.Name, profile.Host+"/"+profile.Organization, err)
	}
	return remoteName, insecureFlag || profile.IsInsecure, nil
}
