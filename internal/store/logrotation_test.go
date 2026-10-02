package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestContainerLogsAreRotatedAsTheDocumentSays holds the deployment's log rotation
// and the operator document to one pair of numbers.
//
// A container log is a copy of personal data that no erasure reaches, so its bound
// is part of the retention statement an operator makes to a guest. An earlier
// `deploy/compose.yaml` set no rotation at all and the document told the
// operator to go and configure one; the two now say the same thing, and this fails
// when a service loses its rotation or the numbers drift from the sentence.
func TestContainerLogsAreRotatedAsTheDocumentSays(t *testing.T) {
	root := filepath.Join("..", "..")
	compose, err := os.ReadFile(filepath.Join(root, "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "operator", "personal-data.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(compose)

	anchor := regexp.MustCompile(`(?m)^x-logging: &logging\n  driver: json-file\n  options:\n    max-size: "(\d+)m"\n    max-file: "(\d+)"\n`).
		FindStringSubmatch(text)
	if anchor == nil {
		t.Fatal("deploy/compose.yaml has no json-file x-logging anchor with max-size and max-file")
	}
	size, files := anchor[1], anchor[2]

	// Every service, found by its two-space indent under `services:`.
	services := text[strings.Index(text, "\nservices:\n"):]
	names := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):\n`).FindAllStringSubmatch(services, -1)
	if len(names) < 4 {
		t.Fatalf("found %d services, want the four this deployment runs", len(names))
	}
	for _, n := range names {
		if !strings.Contains(services, "\n  "+n[1]+":\n    logging: *logging\n") {
			t.Errorf("service %s does not rotate its log (want `logging: *logging` first under it)", n[1])
		}
	}

	want := "at most " + files + " files of\n  " + size + " MB"
	if !strings.Contains(string(doc), want) {
		t.Errorf("docs/operator/personal-data.md does not state the rotation as %q", want)
	}
}
