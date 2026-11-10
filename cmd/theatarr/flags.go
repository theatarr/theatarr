package main

import "flag"

// libraryFlag registers -library and its older spelling -version on one variable. Settings keys and API fields
// call a library a version; the CLI takes both names. Given both, the later one on the command line wins.
func libraryFlag(fs *flag.FlagSet, usage string) *string {
	v := new(string)
	fs.StringVar(v, "library", "", usage)
	fs.StringVar(v, "version", "", "same as -library")
	return v
}
