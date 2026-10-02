package main

import (
	"fmt"

	"github.com/kiennt048/foyer/internal/version"
)

func init() {
	Register(&Command{
		Name:    "version",
		Summary: "Print foyerctl version and the commit it was built from",
		Run: func(_ []string) error {
			// The revision, not only the release. `0.1.0-alpha` is the same string
			// for every build ever made of this release, so it cannot answer the
			// question an operator running this on a host is actually asking.
			fmt.Printf("foyerctl version %s\n", version.String())
			return nil
		},
	})
}
