package events

import "testing"

func FuzzDecodeEvent(f *testing.F) {
	f.Add([]byte(playingBody))
	f.Add([]byte(`{"v":1,"user":"alice","state":"stopped"}`))
	f.Add([]byte(`{"v":1,"user":"alice","state":"paused","track":{"title":"","startMs":-1}}`))
	f.Add([]byte(`{"v":2}`))
	f.Add([]byte(`[]`))

	f.Fuzz(func(t *testing.T, body []byte) {
		event, err := decodeEvent(body)
		if err != nil {
			return
		}
		if event.Version != supportedVersion || !validUser(event.User) {
			t.Fatalf("accepted an invalid header: %+v", event)
		}
		switch event.State {
		case StatePlaying, StatePaused:
			if event.Track == nil {
				t.Fatalf("accepted %s without a track", event.State)
			}
		case StateStopped:
			if event.Track != nil {
				t.Fatal("a stopped event kept its track")
			}
		default:
			t.Fatalf("accepted unknown state %q", event.State)
		}
	})
}
