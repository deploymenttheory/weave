// Flag-parsing helpers shared by the root dispatcher (package main) and
// sub-verb parsers inside this package.
//go:build darwin

package command

import (
	"flag"
	"io"
	"strings"
)

// StringSliceFlag implements repeatable flags (ArgumentParser arrays).
type StringSliceFlag []string

func (f *StringSliceFlag) String() string { return strings.Join(*f, ",") }
func (f *StringSliceFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// ParseInterleaved parses flags and positionals in any order, like
// ArgumentParser (the standard flag package stops at the first positional).
func ParseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, args[0])
		args = args[1:]
	}
}

// NewFlagSet builds a quiet flag set for a subcommand.
func NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
