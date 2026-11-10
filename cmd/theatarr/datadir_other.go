//go:build !unix

package main

import "os"

func dirOwner(os.FileInfo) string { return "" }
