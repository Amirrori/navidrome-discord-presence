package main

import (
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

func TestPlugin(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Navidrome Discord Status Plugin Suite")
}

// Shared matchers and keys for mock expectations across test files.
var (
	spotifyURLKey   = mock.MatchedBy(func(key string) bool { return strings.HasPrefix(key, "spotify.url.") })
	catalogCoverKey = "catalog.cover." + hashKey("weezer\x00weezer")
)
