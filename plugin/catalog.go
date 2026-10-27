package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// Catalog lookups find public album artwork by artist and album name, without
// uploading anything. Results are only used when artist and album match.
const (
	catalogCacheTTLHit  int64 = 30 * 24 * 60 * 60 // 30 days for found artwork
	catalogCacheTTLMiss int64 = 24 * 60 * 60      // 1 day for misses
	catalogTimeoutMs          = 3000
	catalogResultLimit        = "5"

	deezerSearchURL = "https://api.deezer.com/search/album"
	itunesSearchURL = "https://itunes.apple.com/search"

	itunesThumbSize = "100x100bb"
	itunesCoverSize = "600x600bb"
)

type deezerSearchResponse struct {
	Data []struct {
		Title   string `json:"title"`
		CoverXL string `json:"cover_xl"`
		Artist  struct {
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"data"`
}

type itunesSearchResponse struct {
	Results []struct {
		ArtistName     string `json:"artistName"`
		CollectionName string `json:"collectionName"`
		ArtworkURL100  string `json:"artworkUrl100"`
	} `json:"results"`
}

// getImageViaCatalog looks the album up on Deezer, then iTunes, and returns
// the first matching cover URL, or "" if neither has it.
func getImageViaCatalog(albumArtist, artist, album string) string {
	searchArtist := albumArtist
	if searchArtist == "" {
		searchArtist = artist
	}
	if searchArtist == "" || album == "" {
		return ""
	}

	cacheKey := "catalog.cover." + hashKey(strings.ToLower(searchArtist)+"\x00"+strings.ToLower(album))
	if cached, exists, err := host.CacheGetString(cacheKey); err == nil && exists {
		return cached
	}

	coverURL := searchDeezer(searchArtist, album)
	if coverURL == "" {
		coverURL = searchITunes(searchArtist, album)
	}

	ttl := catalogCacheTTLHit
	if coverURL == "" {
		ttl = catalogCacheTTLMiss
	}
	_ = host.CacheSetString(cacheKey, coverURL, ttl)
	return coverURL
}

func searchDeezer(artist, album string) string {
	query := url.Values{}
	query.Set("q", fmt.Sprintf("artist:%q album:%q", artist, album))
	query.Set("limit", catalogResultLimit)

	var result deezerSearchResponse
	if !fetchJSON(deezerSearchURL+"?"+query.Encode(), &result) {
		return ""
	}
	for _, item := range result.Data {
		if catalogMatch(artist, album, item.Artist.Name, item.Title) && item.CoverXL != "" {
			return item.CoverXL
		}
	}
	return ""
}

func searchITunes(artist, album string) string {
	query := url.Values{}
	query.Set("term", artist+" "+album)
	query.Set("entity", "album")
	query.Set("limit", catalogResultLimit)

	var result itunesSearchResponse
	if !fetchJSON(itunesSearchURL+"?"+query.Encode(), &result) {
		return ""
	}
	for _, item := range result.Results {
		if catalogMatch(artist, album, item.ArtistName, item.CollectionName) && item.ArtworkURL100 != "" {
			return strings.Replace(item.ArtworkURL100, itunesThumbSize, itunesCoverSize, 1)
		}
	}
	return ""
}

// fetchJSON performs a GET request and decodes a JSON response into target.
func fetchJSON(requestURL string, target any) bool {
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		TimeoutMs: catalogTimeoutMs,
	})
	if err != nil {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Catalog request failed for %s: %v", requestURL, err))
		return false
	}
	if resp.StatusCode != 200 {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Catalog request returned HTTP %d for %s", resp.StatusCode, requestURL))
		return false
	}
	if err := json.Unmarshal(resp.Body, target); err != nil {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Catalog response not understood for %s: %v", requestURL, err))
		return false
	}
	return true
}

// catalogMatch reports whether a catalog result belongs to the wanted album:
// the artist must match exactly and one album title must contain the other
// (so "Weezer" matches "Weezer (Red Album)"), after normalization.
func catalogMatch(wantArtist, wantAlbum, gotArtist, gotAlbum string) bool {
	if normalizeName(wantArtist) != normalizeName(gotArtist) {
		return false
	}
	want, got := normalizeName(wantAlbum), normalizeName(gotAlbum)
	if want == "" || got == "" {
		return false
	}
	return strings.Contains(got, want) || strings.Contains(want, got)
}

// normalizeName lowercases s and keeps only letters and digits, separated by
// single spaces, so punctuation and spacing differences don't matter.
func normalizeName(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, " ")
}
