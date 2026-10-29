// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"os"
	"strings"
	"testing"
)

// TestImage must be the image the compose file pins, so a digest bump there
// fails here until the tests follow.
func TestTestImageMatchesCompose(t *testing.T) {
	b, err := os.ReadFile("../../deploy/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "image: "+TestImage+"\n") {
		t.Fatalf("deploy/docker-compose.yml does not pin %s", TestImage)
	}
	if !strings.Contains(TestImage, "@sha256:") {
		t.Fatalf("TestImage %s is not pinned by digest", TestImage)
	}
}
