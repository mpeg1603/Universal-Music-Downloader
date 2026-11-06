package main

import (
	"strings"
	"testing"
)

func track(title string, artists []string, album string, seconds float64) *Track {
	return &Track{Key: "k-" + title, Title: title, Artists: artists, Album: album, DurationMs: int(seconds * 1000),
		TrackNumber: 8, TotalTracks: 13, ReleaseDate: "2013-05-17", SpotifyURL: "https://open.spotify.com/track/xyz"}
}

func getLucky() *Track {
	return track("Get Lucky", []string{"Daft Punk"}, "Random Access Memories", 248)
}

func cand(title, artist string, duration float64) Candidate {
	return Candidate{Source: "youtube", URL: "https://example.com/" + title, Title: title, Artist: artist, Duration: duration}
}

func expectAccepted(t *testing.T, tr *Track, c Candidate) {
	t.Helper()
	if m := evaluate(tr, c); m.Reject != "" {
		t.Errorf("%q by %q should match %q, rejected: %s", c.Title, c.Artist, tr.Title, m.Reject)
	}
}

func expectRejected(t *testing.T, tr *Track, c Candidate, why string) {
	t.Helper()
	m := evaluate(tr, c)
	if m.Reject == "" || !strings.Contains(m.Reject, why) {
		t.Errorf("%q by %q should be rejected for %q, got %q (score %.0f)", c.Title, c.Artist, why, m.Reject, m.Score)
	}
}

func TestAcceptsGoodMatches(t *testing.T) {
	expectAccepted(t, getLucky(), cand("Get Lucky (feat. Pharrell Williams & Nile Rodgers)", "Daft Punk, Pharrell Williams, Nile Rodgers", 248))
	expectAccepted(t, getLucky(), cand("Daft Punk - Get Lucky (Official Audio) ft. Pharrell Williams, Nile Rodgers", "DaftPunkVEVO", 250))
	expectAccepted(t, getLucky(), cand("Daft Punk - Get Lucky [HD]", "music lover 1997", 247))
	expectAccepted(t, track("Pyramid Song", []string{"Radiohead"}, "Amnesiac", 289), cand("Pyramid Song", "Radiohead - Topic", 289))
	expectAccepted(t, getLucky(), Candidate{Title: "Get Lucky", Artist: "Daft Punk"}) // unknown length
	expectAccepted(t, track("夜に駆ける", []string{"YOASOBI"}, "THE BOOK", 261), cand("夜に駆ける", "YOASOBI", 261))
	expectAccepted(t, track("Déjà Vu", []string{"Beyoncé"}, "B'Day", 240), cand("Deja Vu", "Beyonce", 241))
	q := track("Bohemian Rhapsody - Remastered 2011", []string{"Queen"}, "A Night at the Opera", 354)
	expectAccepted(t, q, cand("Bohemian Rhapsody (Remastered 2011)", "Queen", 355))
	expectAccepted(t, q, cand("Bohemian Rhapsody", "Queen - Topic", 354))
}

func TestRejectsMashups(t *testing.T) {
	eits := track("Remember Me As A Time Of Day - Remastered", []string{"Explosions In The Sky"}, "How Strange, Innocence", 4)
	expectRejected(t, eits, cand("Ellie Goulding vs. Explosions in the Sky - Lights, Remember Me as a Time of Day", "seancoban", 4), "mashup")
	vs := track("Hello", []string{"Armin van Buuren vs. Tiësto"}, "", 200)
	expectAccepted(t, vs, Candidate{Title: "Armin van Buuren vs. Tiësto - Hello", Artist: "Armin van Buuren", Duration: 200})
}

func TestRejectsOtherVersions(t *testing.T) {
	for _, title := range []string{"Get Lucky (Live at the Grammys)", "Get Lucky - acoustic cover", "Get Lucky Karaoke Version",
		"Get Lucky (sped up)", "Get Lucky [Nightcore]", "Get Lucky 1 Hour", "Get Lucky (Remix)"} {
		expectRejected(t, getLucky(), cand(title, "Daft Punk", 248), "looks like")
	}
	remix := track("Get Lucky - Daft Punk Remix", []string{"Daft Punk"}, "", 250)
	expectAccepted(t, remix, cand("Get Lucky (Daft Punk Remix)", "Daft Punk", 250))
	expectAccepted(t, track("Roxanne", []string{"The Police"}, "Live!", 300), cand("Roxanne (Live)", "The Police", 302))
}

func TestRejectsWrongLengthArtistOrTitle(t *testing.T) {
	expectRejected(t, getLucky(), cand("Get Lucky (Official Video)", "Daft Punk", 369), "length")
	expectRejected(t, getLucky(), cand("Get Lucky", "Daft Punk", 30), "length")
	expectRejected(t, getLucky(), cand("Get Lucky", "Some Other Band", 248), "artist")
	expectRejected(t, getLucky(), cand("Lose Yourself to Dance", "Daft Punk", 250), "title")
	cover := cand("Daft Punk - Get Lucky", "For Orchestra", 248)
	cover.ArtistIsCredit = true // Bandcamp: the uploader is the performer, so this is a cover
	expectRejected(t, getLucky(), cover, "artist")
}

func TestRankOrdersByScore(t *testing.T) {
	good := cand("Get Lucky", "Daft Punk", 248)
	worse := cand("Daft Punk - Get Lucky (Official Video)", "DaftPunkVEVO", 256)
	bad := cand("Get Lucky (Live)", "Daft Punk", 300)
	ok, rejected := rank(getLucky(), []Candidate{worse, bad, good})
	if len(ok) != 2 || ok[0].Cand.Title != good.Title || len(rejected) != 1 || rejected[0].Cand.Title != bad.Title {
		t.Fatalf("unexpected ranking: %+v / %+v", ok, rejected)
	}
	if d := describeRejection(rejected); !strings.Contains(d, "closest was “Get Lucky (Live)” by Daft Punk") {
		t.Errorf("describeRejection = %q", d)
	}
}

func TestTitleHelpers(t *testing.T) {
	cases := map[string]string{
		baseTitle("Song (feat. X) - Radio Edit"):              "Song",
		searchTitle("Song (Bonus) [feat. X] - 2011 Remaster"): "Song",
		cleanTitle("Song (Official Music Video) [HD]", true):  "song",
		cleanTitle("Song | Official Video", true):             "song",
		cleanTitle("Audio", false):                            "audio",
		normalize("Beyoncé & JAY-Z’s “Crazy”"):                "beyonce and jay zs crazy",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestFuzzRatios(t *testing.T) {
	if r := ratio("abc", "abc"); r != 100 {
		t.Errorf("ratio = %v", r)
	}
	if r := ratio("roxanne", "roxanne live"); r < 73 || r > 74 {
		t.Errorf("ratio = %v", r)
	}
	if r := tokenSetRatio("daft punk", "daft punk pharrell williams"); r != 100 {
		t.Errorf("tokenSetRatio = %v", r)
	}
	if r := partialRatio("second song", "artist album 02 second song"); r != 100 {
		t.Errorf("partialRatio = %v", r)
	}
}
