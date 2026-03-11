package spotify

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type DynamicConfig struct {
	TOTPSecret        string
	TOTPVersion       int
	GetTrackHash      string
	FetchPlaylistHash string
	ClientID          string
	ClientVersion     string
	FetchedAt         time.Time
}

type Client struct {
	token          string
	expiryTime     time.Time
	clientToken    string
	clientTokenExp time.Time
	http           *http.Client
	redis          *redis.Client
	ctx            context.Context
	deviceID       string

	configMu sync.Mutex
	config   *DynamicConfig
}

type ExternalURLs struct {
	Spotify string `json:"spotify"`
}

type Image struct {
	URL    string `json:"url"`
	Height int    `json:"height"`
	Width  int    `json:"width"`
}

type SimplifiedArtist struct {
	ExternalURLs ExternalURLs `json:"external_urls"`
	Href         string       `json:"href"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Type         string       `json:"type"`
	URI          string       `json:"uri"`
}

type SimplifiedAlbum struct {
	AlbumType    string             `json:"album_type"`
	Artists      []SimplifiedArtist `json:"artists"`
	ExternalURLs ExternalURLs       `json:"external_urls"`
	Href         string             `json:"href"`
	ID           string             `json:"id"`
	Images       []Image            `json:"images"`
	Name         string             `json:"name"`
	ReleaseDate  string             `json:"release_date"`
	TotalTracks  int                `json:"total_tracks"`
	Type         string             `json:"type"`
	URI          string             `json:"uri"`
}

type Track struct {
	Album        SimplifiedAlbum    `json:"album"`
	Artists      []SimplifiedArtist `json:"artists"`
	DiscNumber   int                `json:"disc_number"`
	DurationMs   int                `json:"duration_ms"`
	Explicit     bool               `json:"explicit"`
	ExternalURLs ExternalURLs       `json:"external_urls"`
	Href         string             `json:"href"`
	ID           string             `json:"id"`
	IsLocal      bool               `json:"is_local"`
	IsPlayable   bool               `json:"is_playable"`
	Name         string             `json:"name"`
	Popularity   int                `json:"popularity"`
	PreviewURL   string             `json:"preview_url"`
	TrackNumber  int                `json:"track_number"`
	Type         string             `json:"type"`
	URI          string             `json:"uri"`
}

type TrackResponse struct {
	Data struct {
		TrackUnion struct {
			Typename     string `json:"__typename"`
			ID           string `json:"id"`
			URI          string `json:"uri"`
			Name         string `json:"name"`
			AlbumOfTrack struct {
				URI      string `json:"uri"`
				Name     string `json:"name"`
				CoverArt struct {
					Sources []struct {
						URL    string `json:"url"`
						Height int    `json:"height"`
						Width  int    `json:"width"`
					} `json:"sources"`
				} `json:"coverArt"`
				Artists struct {
					Items []struct {
						URI     string `json:"uri"`
						Profile struct {
							Name string `json:"name"`
						} `json:"profile"`
					} `json:"items"`
				} `json:"artists"`
			} `json:"albumOfTrack"`
			Artists struct {
				Items []struct {
					URI     string `json:"uri"`
					Profile struct {
						Name string `json:"name"`
					} `json:"profile"`
				} `json:"items"`
			} `json:"artists"`
			Duration struct {
				TotalMilliseconds int `json:"totalMilliseconds"`
			} `json:"duration"`
			Playability struct {
				Playable bool `json:"playable"`
			} `json:"playability"`
		} `json:"trackUnion"`
	} `json:"data"`
}

type PlaylistResponse struct {
	Data struct {
		PlaylistV2 struct {
			Content struct {
				Items []struct {
					ItemV2 struct {
						Data struct {
							Typename     string `json:"__typename"`
							ID           string `json:"id"`
							URI          string `json:"uri"`
							Name         string `json:"name"`
							AlbumOfTrack struct {
								URI      string `json:"uri"`
								Name     string `json:"name"`
								CoverArt struct {
									Sources []struct {
										URL    string `json:"url"`
										Height int    `json:"height"`
										Width  int    `json:"width"`
									} `json:"sources"`
								} `json:"coverArt"`
								Artists struct {
									Items []struct {
										URI     string `json:"uri"`
										Profile struct {
											Name string `json:"name"`
										} `json:"profile"`
									} `json:"items"`
								} `json:"artists"`
							} `json:"albumOfTrack"`
							Artists struct {
								Items []struct {
									URI     string `json:"uri"`
									Profile struct {
										Name string `json:"name"`
									} `json:"profile"`
								} `json:"items"`
							} `json:"artists"`
							TrackDuration struct {
								TotalMilliseconds int `json:"totalMilliseconds"`
							} `json:"trackDuration"`
						} `json:"data"`
					} `json:"itemV2"`
				} `json:"items"`
				TotalCount int `json:"totalCount"`
			} `json:"content"`
		} `json:"playlistV2"`
	} `json:"data"`
}
