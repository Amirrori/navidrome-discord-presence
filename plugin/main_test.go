package main

import (
	"encoding/json"
	"errors"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const testSecret = "0123456789abcdef0123456789abcdef" // gitleaks:allow (test value)

var (
	linkedAnswer   = &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"linked":true}`)}
	unlinkedAnswer = &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"linked":false}`)}
)

func bridgeRequest(path string) any {
	return mock.MatchedBy(func(req host.HTTPRequest) bool {
		return req.URL == "http://bridge.test:8090"+path
	})
}

// sentEvent returns the event posted to the bridge, failing if there was none.
func sentEvent() playbackEvent {
	for _, call := range host.HTTPMock.Calls {
		req := call.Arguments.Get(0).(host.HTTPRequest)
		if req.URL == "http://bridge.test:8090"+eventsPath {
			var event playbackEvent
			Expect(json.Unmarshal(req.Body, &event)).To(Succeed())
			return event
		}
	}
	Fail("no event was sent to the bridge")
	return playbackEvent{}
}

func cacheLinkStatus(username, value string) {
	host.CacheMock.On("GetString", linkedCacheKey(username)).Return(value, value != "", nil)
}

var _ = Describe("statusPlugin", func() {
	var plugin statusPlugin

	BeforeEach(func() {
		pdk.ResetMock()
		host.HTTPMock.ExpectedCalls = nil
		host.HTTPMock.Calls = nil
		host.CacheMock.ExpectedCalls = nil
		host.CacheMock.Calls = nil
		host.SubsonicAPIMock.ExpectedCalls = nil
		host.SubsonicAPIMock.Calls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
		pdk.PDKMock.On("GetConfig", bridgeURLKey).Return("http://bridge.test:8090", true)
		pdk.PDKMock.On("GetConfig", bridgeSecretKey).Return(testSecret, true)
		pdk.PDKMock.On("GetConfig", spotifyLinksKey).Return("", false)
		pdk.PDKMock.On("GetConfig", caaEnabledKey).Return("", false)
		pdk.PDKMock.On("GetConfig", catalogLookupKey).Return("", false)
		pdk.PDKMock.On("GetConfig", uguuEnabledKey).Return("", false)
		host.CacheMock.On("SetString", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	})

	Describe("IsAuthorized", func() {
		It("accepts a user the bridge reported as linked", func() {
			cacheLinkStatus("alice", linkedValue)
			Expect(plugin.IsAuthorized(scrobbler.IsAuthorizedRequest{Username: "alice"})).To(BeTrue())
			host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
		})

		It("refuses a user the bridge reported as not linked", func() {
			cacheLinkStatus("bob", unlinkedValue)
			Expect(plugin.IsAuthorized(scrobbler.IsAuthorizedRequest{Username: "bob"})).To(BeFalse())
		})

		It("asks the bridge once and caches the answer", func() {
			cacheLinkStatus("carol", "")
			host.HTTPMock.On("Send", bridgeRequest(linkedPath)).Return(unlinkedAnswer, nil)

			Expect(plugin.IsAuthorized(scrobbler.IsAuthorizedRequest{Username: "carol"})).To(BeFalse())
			host.CacheMock.AssertCalled(GinkgoT(), "SetString", linkedCacheKey("carol"), unlinkedValue, unlinkedCacheTTL)
		})

		It("refuses when the bridge can't be asked", func() {
			cacheLinkStatus("alice", "")
			host.HTTPMock.On("Send", bridgeRequest(linkedPath)).Return((*host.HTTPResponse)(nil), errors.New("connection refused"))

			Expect(plugin.IsAuthorized(scrobbler.IsAuthorizedRequest{Username: "alice"})).To(BeFalse())
			host.CacheMock.AssertNotCalled(GinkgoT(), "SetString", linkedCacheKey("alice"), mock.Anything, mock.Anything)
		})
	})

	Describe("PlaybackReport", func() {
		playing := scrobbler.PlaybackReportRequest{
			Username:     "alice",
			State:        statePlaying,
			PositionMs:   30_000,
			PlaybackRate: 1.0,
			Timestamp:    1_700_000_000,
			Track: scrobbler.TrackInfo{
				Title:    "Undone",
				Artist:   "Weezer",
				Album:    "Weezer",
				Duration: 200,
			},
		}

		Context("for a linked user", func() {
			BeforeEach(func() {
				cacheLinkStatus("alice", linkedValue)
			})

			It("sends a signed playing event with the track and its time window", func() {
				host.HTTPMock.On("Send", bridgeRequest(eventsPath)).Return(linkedAnswer, nil)

				Expect(plugin.PlaybackReport(playing)).To(Succeed())

				event := sentEvent()
				Expect(event.Version).To(Equal(eventVersion))
				Expect(event.Username).To(Equal("alice"))
				Expect(event.State).To(Equal(statePlaying))
				Expect(event.Track.Title).To(Equal("Undone"))
				Expect(event.Track.StartMs).To(Equal(int64(1_700_000_000_000 - 30_000)))
				Expect(event.Track.EndMs).To(Equal(int64(1_700_000_000_000 - 30_000 + 200_000)))

				req := host.HTTPMock.Calls[0].Arguments.Get(0).(host.HTTPRequest)
				Expect(req.Headers[headerSignature]).To(Equal(
					signatureScheme + sign([]byte(testSecret), req.Headers[headerTimestamp], req.Headers[headerNonce], req.Body)))
				Expect(req.NoFollowRedirects).To(BeTrue())
			})

			It("treats the starting state like playing", func() {
				host.HTTPMock.On("Send", bridgeRequest(eventsPath)).Return(linkedAnswer, nil)
				starting := playing
				starting.State = stateStarting

				Expect(plugin.PlaybackReport(starting)).To(Succeed())
				Expect(sentEvent().State).To(Equal(statePlaying))
			})

			It("sends a stopped event without track details", func() {
				host.HTTPMock.On("Send", bridgeRequest(eventsPath)).Return(linkedAnswer, nil)
				expired := playing
				expired.State = stateExpired

				Expect(plugin.PlaybackReport(expired)).To(Succeed())
				event := sentEvent()
				Expect(event.State).To(Equal(stateStopped))
				Expect(event.Track).To(BeNil())
			})

			It("remembers when the bridge reports the user as unlinked", func() {
				host.HTTPMock.On("Send", bridgeRequest(eventsPath)).Return(unlinkedAnswer, nil)

				Expect(plugin.PlaybackReport(playing)).To(Succeed())
				host.CacheMock.AssertCalled(GinkgoT(), "SetString", linkedCacheKey("alice"), unlinkedValue, unlinkedCacheTTL)
			})

			It("ignores unknown states", func() {
				unknown := playing
				unknown.State = "rewinding"

				Expect(plugin.PlaybackReport(unknown)).To(Succeed())
				host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
			})

			It("never reports bridge failures to Navidrome", func() {
				host.HTTPMock.On("Send", bridgeRequest(eventsPath)).Return((*host.HTTPResponse)(nil), errors.New("connection refused"))

				Expect(plugin.PlaybackReport(playing)).To(Succeed())
			})
		})

		It("does nothing at all for a user who hasn't linked", func() {
			cacheLinkStatus("alice", unlinkedValue)
			pdk.PDKMock.ExpectedCalls = nil
			pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
			pdk.PDKMock.On("GetConfig", bridgeURLKey).Return("http://bridge.test:8090", true)
			pdk.PDKMock.On("GetConfig", bridgeSecretKey).Return(testSecret, true)
			// Every artwork source is enabled: none of them may be used.
			pdk.PDKMock.On("GetConfig", mock.Anything).Return("true", true)

			Expect(plugin.PlaybackReport(playing)).To(Succeed())
			host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
			host.SubsonicAPIMock.AssertNotCalled(GinkgoT(), "CallRaw", mock.Anything)
		})

		It("sends nothing while the bridge isn't configured", func() {
			pdk.PDKMock.ExpectedCalls = nil
			pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
			pdk.PDKMock.On("GetConfig", bridgeURLKey).Return("", false)
			pdk.PDKMock.On("GetConfig", bridgeSecretKey).Return("", false)

			Expect(plugin.PlaybackReport(playing)).To(Succeed())
			host.HTTPMock.AssertNotCalled(GinkgoT(), "Send", mock.Anything)
		})
	})
})
