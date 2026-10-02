package main

import (
	"os"

	"github.com/kiennt048/foyer/internal/version"
)

// Version is the foyerctl build version.
//
// It is an alias of `internal/version` rather than a second copy of the string,
// so the support bundle and the `version` command cannot disagree with the
// control plane about which Foyer this is.
const Version = version.Version

func main() {
	os.Exit(dispatch(os.Args))
}
