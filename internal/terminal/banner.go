// The Weave banner: a box-drawing wordmark (in the spirit of lume's
// Main.swift banner) spelling "WEAVE". Shown on no-args and help invocations.
//go:build darwin

package terminal

import (
	"fmt"
	"io"
	"strings"

	"github.com/deploymenttheory/weave/internal/ci"
)

// weaveArt is the wordmark rendered from Plus Jakarta Sans Regular (weight 400),
// converted to Unicode 8-dot Braille. Each line is the same cell width so text
// can be appended to the right of the middle rows.
var weaveArt = [6]string{
	"⢿⣧⠀⠀⠀⠀⣸⣿⡇⠀⠀⠀⠀⣼⡿⠀⠀⢀⣴⣾⠿⠿⢷⣦⣄⠀⠀⠀⠀⢀⣴⣶⠿⠿⢿⣶⣄⠀⠀⢿⣧⠀⠀⠀⠀⠀⢰⣿⠃⠀⠀⣠⣶⡿⠿⠿⣶⣤⡀⠀",
	"⠘⣿⡆⠀⠀⢀⣿⠻⣿⡀⠀⠀⢰⣿⠃⠀⣰⡿⠋⠀⠀⠀⠀⠙⢿⣆⠀⠀⠀⠙⠋⠀⠀⠀⠀⠘⣿⡆⠀⠈⣿⣆⠀⠀⠀⢠⣿⠏⠀⢀⣾⠟⠁⠀⠀⠀⠈⠻⣷⡀",
	"⠀⢹⣿⠀⠀⣼⡏⠀⢻⣧⠀⢀⣿⡏⠀⠀⣿⣷⣶⣶⣶⣶⣶⣶⣾⣿⠀⠀⠀⢀⣠⣤⣶⡶⠶⠿⣿⡇⠀⠀⠸⣿⡄⠀⠀⣾⡟⠀⠀⢸⣿⣶⣶⣶⣶⣶⣶⣶⣿⡇",
	"⠀⠀⢿⣇⢰⣿⠁⠀⠈⣿⡄⣼⡿⠀⠀⠀⢻⣧⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢠⣿⠋⠀⠀⠀⠀⢠⣿⡇⠀⠀⠀⢹⣷⠀⣸⡿⠀⠀⠀⠘⣿⡄⠀⠀⠀⠀⠀⠀⠀⠀",
	"⠀⠀⠘⣿⣿⡇⠀⠀⠀⢹⣿⣿⠃⠀⠀⠀⠈⠻⣷⣤⣀⣀⣠⣴⡿⠃⠀⠀⠘⣿⣦⣀⣀⣠⣴⡿⣿⡇⠀⠀⠀⠀⢿⣷⣿⠃⠀⠀⠀⠀⠙⢿⣦⣄⣀⣀⣤⣾⠟⠀",
	"⠀⠀⠀⠙⠛⠀⠀⠀⠀⠀⠛⠋⠀⠀⠀⠀⠀⠀⠈⠙⠛⠛⠛⠉⠀⠀⠀⠀⠀⠈⠙⠛⠛⠋⠁⠀⠛⠃⠀⠀⠀⠀⠈⠛⠃⠀⠀⠀⠀⠀⠀⠀⠉⠛⠛⠛⠋⠁⠀⠀",
}

// PrintBanner writes the Weave banner to w, with the version beside the
// wordmark and a tagline beneath it.
func PrintBanner(w io.Writer) {
	version := ci.CIVersion()
	if len(version) > 0 && version[0] >= '0' && version[0] <= '9' {
		version = "v" + version // numeric releases read "Weave v1.2.3"
	}
	for i, line := range weaveArt {
		switch i {
		case 2:
			fmt.Fprintln(w, blue("  "+line)+bold("   Weave "+version))
		case 3:
			fmt.Fprintln(w, blue("  "+line)+"   macOS & Linux VM CLI and server")
		default:
			fmt.Fprintln(w, blue("  "+line))
		}
	}
}

// PrintBannerWithUsage prints the banner followed by the subcommand list.
func PrintBannerWithUsage(w io.Writer, subcommands []string) {
	PrintBanner(w)
	fmt.Fprintf(w, "\nUsage: weave <subcommand> [options]\n\nSubcommands:\n  %s\n",
		strings.Join(subcommands, ", "))
}
