package main

import (
	"fmt"

	"github.com/theatarr/theatarr/internal/buildinfo"
)

func init() {
	register(command{
		name:    "version",
		summary: "print the version and build revision",
		run: func([]string) error {
			fmt.Println(buildinfo.String())
			return nil
		},
	})
}
