package main

import (
	"errors"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("headCoverArt", func() {
	BeforeEach(func() {
		pdk.ResetMock()
		host.HTTPMock.ExpectedCalls = nil
		host.HTTPMock.Calls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
	})

	It("returns Location header and definitive=true on 307 response", func() {
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.Method == "HEAD" &&
				req.URL == "https://coverartarchive.org/release/test-mbid/front-500" &&
				req.NoFollowRedirects == true
		})).Return(&host.HTTPResponse{
			StatusCode: 307,
			Headers:    map[string]string{"Location": "https://archive.org/download/mbid-test/thumb500.jpg"},
		}, nil)

		result, definitive := headCoverArt("https://coverartarchive.org/release/test-mbid/front-500")
		Expect(result).To(Equal("https://archive.org/download/mbid-test/thumb500.jpg"))
		Expect(definitive).To(BeTrue())
	})

	It("returns empty and definitive=true on 404 response", func() {
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.Method == "HEAD" && req.NoFollowRedirects == true
		})).Return(&host.HTTPResponse{StatusCode: 404}, nil)

		result, definitive := headCoverArt("https://coverartarchive.org/release/no-art/front-500")
		Expect(result).To(BeEmpty())
		Expect(definitive).To(BeTrue())
	})

	It("returns empty and definitive=false on HTTP error", func() {
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.Method == "HEAD"
		})).Return((*host.HTTPResponse)(nil), errors.New("connection refused"))

		result, definitive := headCoverArt("https://coverartarchive.org/release/err/front-500")
		Expect(result).To(BeEmpty())
		Expect(definitive).To(BeFalse())
	})

	It("returns empty and definitive=true when Location header is missing on 307", func() {
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.Method == "HEAD"
		})).Return(&host.HTTPResponse{
			StatusCode: 307,
			Headers:    map[string]string{},
		}, nil)

		result, definitive := headCoverArt("https://coverartarchive.org/release/no-location/front-500")
		Expect(result).To(BeEmpty())
		Expect(definitive).To(BeTrue())
	})
})

var _ = Describe("getImageURL", func() {
	track := scrobbler.TrackInfo{
		ID:                "track1",
		Artist:            "Weezer",
		Album:             "Weezer",
		MBZAlbumID:        "album-id",
		MBZReleaseGroupID: "rg-id",
	}

	setSources := func(caa, catalog, uguu bool) {
		value := func(on bool) (string, bool) {
			if on {
				return "true", true
			}
			return "", false
		}
		v, ok := value(caa)
		pdk.PDKMock.On("GetConfig", caaEnabledKey).Return(v, ok)
		v, ok = value(catalog)
		pdk.PDKMock.On("GetConfig", catalogLookupKey).Return(v, ok)
		v, ok = value(uguu)
		pdk.PDKMock.On("GetConfig", uguuEnabledKey).Return(v, ok)
	}

	BeforeEach(func() {
		pdk.ResetMock()
		host.CacheMock.ExpectedCalls = nil
		host.CacheMock.Calls = nil
		host.SubsonicAPIMock.ExpectedCalls = nil
		host.SubsonicAPIMock.Calls = nil
		host.HTTPMock.ExpectedCalls = nil
		host.HTTPMock.Calls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
	})

	It("returns empty without any network call when all sources are disabled", func() {
		setSources(false, false, false)

		Expect(getImageURL("testuser", track)).To(BeEmpty())
		host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
	})

	It("prefers the Cover Art Archive when it has artwork", func() {
		setSources(true, true, true)
		host.CacheMock.On("GetString", "caa.artwork.album-id").Return("https://archive.org/art.jpg", true, nil)

		Expect(getImageURL("testuser", track)).To(Equal("https://archive.org/art.jpg"))
		host.SubsonicAPIMock.AssertNotCalled(GinkgoT(), "CallRaw", mock.Anything)
	})

	It("falls back to the catalog lookup when CAA has nothing", func() {
		setSources(true, true, true)
		host.CacheMock.On("GetString", "caa.artwork.album-id").Return("", true, nil)
		host.CacheMock.On("GetString", catalogCoverKey).Return("https://cdn-images.dzcdn.net/cover.jpg", true, nil)

		Expect(getImageURL("testuser", track)).To(Equal("https://cdn-images.dzcdn.net/cover.jpg"))
		host.SubsonicAPIMock.AssertNotCalled(GinkgoT(), "CallRaw", mock.Anything)
	})

	It("uploads to uguu.se only when the other sources have nothing", func() {
		setSources(false, true, true)
		host.CacheMock.On("GetString", catalogCoverKey).Return("", true, nil)
		host.CacheMock.On("GetString", "uguu.artwork.track1").Return("https://a.uguu.se/cached.jpg", true, nil)

		Expect(getImageURL("testuser", track)).To(Equal("https://a.uguu.se/cached.jpg"))
	})

	It("uploads artwork to uguu.se and caches the result", func() {
		setSources(false, false, true)
		host.CacheMock.On("GetString", "uguu.artwork.track1").Return("", false, nil)
		host.SubsonicAPIMock.On("CallRaw", "/getCoverArt?u=testuser&id=track1&size=300").
			Return("image/jpeg", []byte("fake-image-data"), nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://uguu.se/upload"
		})).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"files":[{"url":"https://a.uguu.se/uploaded.jpg"}]}`)}, nil)
		host.CacheMock.On("SetString", "uguu.artwork.track1", "https://a.uguu.se/uploaded.jpg", uguuCacheTTL).Return(nil)

		Expect(getImageURL("testuser", track)).To(Equal("https://a.uguu.se/uploaded.jpg"))
	})

	It("returns empty when the uguu.se upload fails", func() {
		setSources(false, false, true)
		host.CacheMock.On("GetString", "uguu.artwork.track1").Return("", false, nil)
		host.SubsonicAPIMock.On("CallRaw", "/getCoverArt?u=testuser&id=track1&size=300").
			Return("image/jpeg", []byte("fake-image-data"), nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://uguu.se/upload"
		})).Return(&host.HTTPResponse{StatusCode: 500, Body: []byte(`{"success":false}`)}, nil)

		Expect(getImageURL("testuser", track)).To(BeEmpty())
	})

	It("returns empty when the artwork can't be read from Navidrome", func() {
		setSources(false, false, true)
		host.CacheMock.On("GetString", "uguu.artwork.track1").Return("", false, nil)
		host.SubsonicAPIMock.On("CallRaw", "/getCoverArt?u=testuser&id=track1&size=300").
			Return("", []byte(nil), errors.New("fetch failed"))

		Expect(getImageURL("testuser", track)).To(BeEmpty())
	})
})

var _ = Describe("getImageViaCoverArt", func() {
	BeforeEach(func() {
		pdk.ResetMock()
		host.CacheMock.ExpectedCalls = nil
		host.CacheMock.Calls = nil
		host.HTTPMock.ExpectedCalls = nil
		host.HTTPMock.Calls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
	})

	It("returns cached URL on cache hit", func() {
		host.CacheMock.On("GetString", "caa.artwork.album-123").Return("https://archive.org/cached.jpg", true, nil)

		result := getImageViaCoverArt("album-123", "rg-456")
		Expect(result).To(Equal("https://archive.org/cached.jpg"))
		host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
	})

	It("returns empty on cache hit with empty string (known miss)", func() {
		host.CacheMock.On("GetString", "caa.artwork.album-123").Return("", true, nil)

		result := getImageViaCoverArt("album-123", "rg-456")
		Expect(result).To(BeEmpty())
		host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
	})

	It("returns release URL on 307 and caches it", func() {
		host.CacheMock.On("GetString", "caa.artwork.album-123").Return("", false, nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release/album-123/front-500"
		})).Return(&host.HTTPResponse{
			StatusCode: 307,
			Headers:    map[string]string{"Location": "https://archive.org/release-art.jpg"},
		}, nil)
		host.CacheMock.On("SetString", "caa.artwork.album-123", "https://archive.org/release-art.jpg", int64(86400)).Return(nil)

		result := getImageViaCoverArt("album-123", "rg-456")
		Expect(result).To(Equal("https://archive.org/release-art.jpg"))
		host.CacheMock.AssertCalled(GinkgoT(), "SetString", "caa.artwork.album-123", "https://archive.org/release-art.jpg", int64(86400))
	})

	It("falls back to release-group when release returns 404", func() {
		host.CacheMock.On("GetString", "caa.artwork.album-123").Return("", false, nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release/album-123/front-500"
		})).Return(&host.HTTPResponse{StatusCode: 404}, nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release-group/rg-456/front-500"
		})).Return(&host.HTTPResponse{
			StatusCode: 307,
			Headers:    map[string]string{"Location": "https://archive.org/rg-art.jpg"},
		}, nil)
		host.CacheMock.On("SetString", "caa.artwork.album-123", "https://archive.org/rg-art.jpg", int64(86400)).Return(nil)

		result := getImageViaCoverArt("album-123", "rg-456")
		Expect(result).To(Equal("https://archive.org/rg-art.jpg"))
	})

	It("caches empty string when both release and release-group fail", func() {
		host.CacheMock.On("GetString", "caa.artwork.album-123").Return("", false, nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release/album-123/front-500"
		})).Return(&host.HTTPResponse{StatusCode: 404}, nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release-group/rg-456/front-500"
		})).Return(&host.HTTPResponse{StatusCode: 404}, nil)
		host.CacheMock.On("SetString", "caa.artwork.album-123", "", int64(14400)).Return(nil)

		result := getImageViaCoverArt("album-123", "rg-456")
		Expect(result).To(BeEmpty())
		host.CacheMock.AssertCalled(GinkgoT(), "SetString", "caa.artwork.album-123", "", int64(14400))
	})

	It("does not cache miss on transient failure", func() {
		host.CacheMock.On("GetString", "caa.artwork.album-123").Return("", false, nil)
		// Both requests fail with network errors (transient)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release/album-123/front-500"
		})).Return((*host.HTTPResponse)(nil), errors.New("connection refused"))
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release-group/rg-456/front-500"
		})).Return((*host.HTTPResponse)(nil), errors.New("timeout"))

		result := getImageViaCoverArt("album-123", "rg-456")
		Expect(result).To(BeEmpty())
		// Should NOT cache the miss since failures were transient
		host.CacheMock.AssertNotCalled(GinkgoT(), "SetString", mock.Anything, mock.Anything, mock.Anything)
	})

	It("tries only release-group when MBZAlbumID is empty", func() {
		host.CacheMock.On("GetString", "caa.artwork.rg.rg-456").Return("", false, nil)
		host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
			return req.URL == "https://coverartarchive.org/release-group/rg-456/front-500"
		})).Return(&host.HTTPResponse{
			StatusCode: 307,
			Headers:    map[string]string{"Location": "https://archive.org/rg-art.jpg"},
		}, nil)
		host.CacheMock.On("SetString", "caa.artwork.rg.rg-456", "https://archive.org/rg-art.jpg", int64(86400)).Return(nil)

		result := getImageViaCoverArt("", "rg-456")
		Expect(result).To(Equal("https://archive.org/rg-art.jpg"))
	})

	It("returns empty when both IDs are empty", func() {
		result := getImageViaCoverArt("", "")
		Expect(result).To(BeEmpty())
		host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
		host.CacheMock.AssertNotCalled(GinkgoT(), "GetString", mock.Anything)
	})
})
