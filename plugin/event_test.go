package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("playbackWindow", func() {
	report := scrobbler.PlaybackReportRequest{
		PositionMs:   60_000,
		PlaybackRate: 1.0,
		Timestamp:    1_700_000_000,
		Track:        scrobbler.TrackInfo{Duration: 180},
	}
	const reportedAtMs = int64(1_700_000_000_000)

	It("places the start before the report by the elapsed time", func() {
		start, end := playbackWindow(report, false)
		Expect(start).To(Equal(reportedAtMs - 60_000))
		Expect(end).To(Equal(reportedAtMs - 60_000 + 180_000))
	})

	It("scales elapsed time and duration by the playback rate", func() {
		fast := report
		fast.PlaybackRate = 2.0

		start, end := playbackWindow(fast, false)
		Expect(start).To(Equal(reportedAtMs - 30_000))
		Expect(end).To(Equal(reportedAtMs - 30_000 + 90_000))
	})

	It("treats a missing playback rate as normal speed", func() {
		unknown := report
		unknown.PlaybackRate = 0

		start, _ := playbackWindow(unknown, false)
		Expect(start).To(Equal(reportedAtMs - 60_000))
	})

	It("only sets the pause moment while paused", func() {
		start, end := playbackWindow(report, true)
		Expect(start).To(Equal(reportedAtMs))
		Expect(end).To(BeZero())
	})
})
