package main

import (
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("catalog lookup", func() {
	isDeezer := mock.MatchedBy(func(req host.HTTPRequest) bool { return strings.HasPrefix(req.URL, deezerSearchURL) })
	isITunes := mock.MatchedBy(func(req host.HTTPRequest) bool { return strings.HasPrefix(req.URL, itunesSearchURL) })

	BeforeEach(func() {
		pdk.ResetMock()
		host.HTTPMock.ExpectedCalls = nil
		host.HTTPMock.Calls = nil
		host.CacheMock.ExpectedCalls = nil
		host.CacheMock.Calls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
		host.CacheMock.On("GetString", catalogCoverKey).Return("", false, nil)
		host.CacheMock.On("SetString", catalogCoverKey, mock.Anything, mock.Anything).Return(nil)
	})

	It("uses the matching Deezer result", func() {
		host.HTTPMock.On("Send", isDeezer).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[
			{"title":"Pinkerton","cover_xl":"https://cdn-images.dzcdn.net/wrong.jpg","artist":{"name":"Weezer"}},
			{"title":"Weezer (Red Album)","cover_xl":"https://cdn-images.dzcdn.net/red.jpg","artist":{"name":"Weezer"}}
		]}`)}, nil)

		Expect(getImageViaCatalog("", "Weezer", "Weezer")).To(Equal("https://cdn-images.dzcdn.net/red.jpg"))
		host.CacheMock.AssertCalled(GinkgoT(), "SetString", catalogCoverKey, "https://cdn-images.dzcdn.net/red.jpg", catalogCacheTTLHit)
	})

	It("falls back to iTunes and requests a larger image", func() {
		host.HTTPMock.On("Send", isDeezer).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[]}`)}, nil)
		host.HTTPMock.On("Send", isITunes).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"results":[
			{"artistName":"Weezer","collectionName":"Weezer","artworkUrl100":"https://is1-ssl.mzstatic.com/x/100x100bb.jpg"}
		]}`)}, nil)

		Expect(getImageViaCatalog("", "Weezer", "Weezer")).To(Equal("https://is1-ssl.mzstatic.com/x/600x600bb.jpg"))
	})

	It("rejects results from another artist and caches the miss", func() {
		host.HTTPMock.On("Send", isDeezer).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[
			{"title":"Weezer","cover_xl":"https://cdn-images.dzcdn.net/tribute.jpg","artist":{"name":"Tribute Band"}}
		]}`)}, nil)
		host.HTTPMock.On("Send", isITunes).Return(&host.HTTPResponse{StatusCode: 503}, nil)

		Expect(getImageViaCatalog("", "Weezer", "Weezer")).To(BeEmpty())
		host.CacheMock.AssertCalled(GinkgoT(), "SetString", catalogCoverKey, "", catalogCacheTTLMiss)
	})

	It("searches by album artist when it is set", func() {
		various := "catalog.cover." + hashKey("various artists\x00weezer")
		host.CacheMock.On("GetString", various).Return("https://cdn-images.dzcdn.net/cached.jpg", true, nil)

		Expect(getImageViaCatalog("Various Artists", "Weezer", "Weezer")).To(Equal("https://cdn-images.dzcdn.net/cached.jpg"))
		host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
	})

	It("skips the lookup without an album name", func() {
		Expect(getImageViaCatalog("", "Weezer", "")).To(BeEmpty())
		host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
	})

	DescribeTable("catalogMatch",
		func(wantArtist, wantAlbum, gotArtist, gotAlbum string, expected bool) {
			Expect(catalogMatch(wantArtist, wantAlbum, gotArtist, gotAlbum)).To(Equal(expected))
		},
		Entry("identical", "Weezer", "Pinkerton", "Weezer", "Pinkerton", true),
		Entry("edition suffix", "Weezer", "Weezer", "Weezer", "Weezer (Deluxe Edition)", true),
		Entry("punctuation and case", "AC/DC", "Back in Black", "ac dc", "BACK IN BLACK", true),
		Entry("non-Latin names", "目黒将司", "ペルソナ3", "目黒将司", "ペルソナ3 オリジナル", true),
		Entry("different artist", "Weezer", "Weezer", "Tribute Band", "Weezer", false),
		Entry("different album", "Weezer", "Pinkerton", "Weezer", "Maladroit", false),
		Entry("empty album", "Weezer", "", "Weezer", "Weezer", false),
	)
})
