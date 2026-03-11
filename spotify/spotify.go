package spotify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

var linkRegex = regexp.MustCompile(`open\.spotify\.com\/(track|playlist|album|artist)\/([a-zA-Z0-9]+)`)

func NewClient(proxyURLStr string, rdb *redis.Client) *Client {
	transport := &http.Transport{}
	if proxyURLStr != "" {
		proxyURL, err := url.Parse(proxyURLStr)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		} else {
			logrus.WithError(err).Warn("Invalid proxy URL provided, ignoring")
		}
	}

	c := &Client{
		http: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		redis: rdb,
		ctx:   context.Background(),
	}

	c.initDeviceID()

	return c
}

func (c *Client) initDeviceID() {
	if c.redis != nil {
		if val, err := c.redis.Get(c.ctx, "spoxy:device_id").Result(); err == nil && val != "" {
			c.deviceID = val
			logrus.WithField("device_id", c.deviceID).Debug("Using persistent device_id from Redis")
			return
		}
	}

	c.deviceID = generateDeviceID()
	if c.redis != nil {
		c.redis.Set(c.ctx, "spoxy:device_id", c.deviceID, 0)
	}
	logrus.WithField("device_id", c.deviceID).Debug("Generated and persisted new device_id")
}

func generateDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var (
	jsUrlRegex             = regexp.MustCompile(`src="([^"]+web-player[^"]+\.js)"`)
	totpSecretRegex        = regexp.MustCompile(`\{secret:['"]([^'"]+)['"],version:(\d+)\}`)
	getTrackHashRegex      = regexp.MustCompile(`"getTrack",\s*"query",\s*"([a-f0-9]{64})"`)
	getAlbumHashRegex      = regexp.MustCompile(`"getAlbum",\s*"query",\s*"([a-f0-9]{64})"`)
	getArtistHashRegex     = regexp.MustCompile(`"queryArtistOverview",\s*"query",\s*"([a-f0-9]{64})"`)
	fetchPlaylistHashRegex = regexp.MustCompile(`"fetchPlaylist",\s*"query",\s*"([a-f0-9]{64})"`)
	clientIDRegex          = regexp.MustCompile(`clientId:"([a-f0-9]{32})"`)
	clientVersionRegex     = regexp.MustCompile(`client_version:"([^"]+)"`)
)

func (c *Client) getDynamicConfig() (*DynamicConfig, error) {
	c.configMu.Lock()
	defer c.configMu.Unlock()

	// Cache for 24 hours
	if c.config != nil && time.Since(c.config.FetchedAt) < 24*time.Hour {
		return c.config, nil
	}

	logrus.Info("Fetching new dynamic configuration from Spotify web player")

	// 1. Fetch HTML
	req, _ := http.NewRequest("GET", "https://open.spotify.com/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:148.0) Gecko/20100101 Firefox/148.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Spotify HTML: %w", err)
	}
	defer resp.Body.Close()

	htmlBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTML body: %w", err)
	}

	// 2. Extract JS bundle URL
	matches := jsUrlRegex.FindStringSubmatch(string(htmlBytes))
	if len(matches) < 2 {
		return nil, fmt.Errorf("could not find web-player script URL in HTML")
	}
	jsUrl := matches[1]
	if !regexp.MustCompile(`^https?://`).MatchString(jsUrl) {
		// handle relative URLs if needed, though they usually seem to be absolute CDN links
		if jsUrl[0] == '/' {
			jsUrl = "https://open.spotify.com" + jsUrl
		}
	}

	logrus.WithField("js_url", jsUrl).Debug("Found web-player script")

	// 3. Fetch JS bundle
	reqJS, _ := http.NewRequest("GET", jsUrl, nil)
	reqJS.Header.Set("User-Agent", "Mozilla/5.0")
	respJS, err := c.http.Do(reqJS)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JS bundle: %w", err)
	}
	defer respJS.Body.Close()

	jsBytes, err := io.ReadAll(respJS.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read JS body: %w", err)
	}
	jsContent := string(jsBytes)

	// 4. Extract secrets and hashes
	newConfig := &DynamicConfig{
		FetchedAt: time.Now(),
	}

	totpMatches := totpSecretRegex.FindStringSubmatch(jsContent)
	if len(totpMatches) >= 3 {
		newConfig.TOTPSecret = totpMatches[1]
		fmt.Sscanf(totpMatches[2], "%d", &newConfig.TOTPVersion)
	} else {
		return nil, fmt.Errorf("could not find TOTP secret in JS bundle")
	}

	trackHashMatches := getTrackHashRegex.FindStringSubmatch(jsContent)
	if len(trackHashMatches) >= 2 {
		newConfig.GetTrackHash = trackHashMatches[1]
	} else {
		return nil, fmt.Errorf("could not find getTrack hash in JS bundle")
	}

	albumHashMatches := getAlbumHashRegex.FindStringSubmatch(jsContent)
	if len(albumHashMatches) >= 2 {
		newConfig.GetAlbumHash = albumHashMatches[1]
	} else {
		return nil, fmt.Errorf("could not find getAlbum hash in JS bundle")
	}

	artistHashMatches := getArtistHashRegex.FindStringSubmatch(jsContent)
	if len(artistHashMatches) >= 2 {
		newConfig.GetArtistHash = artistHashMatches[1]
	} else {
		return nil, fmt.Errorf("could not find getArtist hash in JS bundle")
	}

	playlistHashMatches := fetchPlaylistHashRegex.FindStringSubmatch(jsContent)
	if len(playlistHashMatches) >= 2 {
		newConfig.FetchPlaylistHash = playlistHashMatches[1]
	} else {
		return nil, fmt.Errorf("could not find fetchPlaylist hash in JS bundle")
	}

	// Extract client_id and client_version (non-fatal fallback to hardcoded values)
	if m := clientIDRegex.FindStringSubmatch(jsContent); len(m) >= 2 {
		newConfig.ClientID = m[1]
	} else {
		newConfig.ClientID = "d8a5ed958d274c2e8ee717e6a4b0971d" // fallback
	}
	if m := clientVersionRegex.FindStringSubmatch(jsContent); len(m) >= 2 {
		newConfig.ClientVersion = m[1]
	} else {
		newConfig.ClientVersion = "1.2.86.152.gcb85a522" // fallback
	}

	logrus.WithFields(logrus.Fields{
		"totp_version":  newConfig.TOTPVersion,
		"track_hash":    newConfig.GetTrackHash[:8] + "...",
		"album_hash":    newConfig.GetAlbumHash[:8] + "...",
		"artist_hash":   newConfig.GetArtistHash[:8] + "...",
		"playlist_hash": newConfig.FetchPlaylistHash[:8] + "...",
		"client_id":     newConfig.ClientID[:8] + "...",
	}).Info("Successfully extracted dynamic configuration")

	c.config = newConfig
	return c.config, nil
}

func (c *Client) ensureClientToken() error {
	if c.clientToken != "" && time.Now().Before(c.clientTokenExp) {
		return nil
	}

	// Try fetching from Redis cache first
	if c.redis != nil {
		if val, err := c.redis.Get(c.ctx, "spoxy:client_token").Result(); err == nil && val != "" {
			if exp, err := c.redis.Get(c.ctx, "spoxy:client_token_exp").Result(); err == nil && exp != "" {
				expTime, err := time.Parse(time.RFC3339, exp)
				if err == nil && time.Now().Before(expTime) {
					c.clientToken = val
					c.clientTokenExp = expTime
					logrus.Debug("Using cached Spotify client token")
					return nil
				}
			}
		}
	}

	config, err := c.getDynamicConfig()
	if err != nil {
		// If we can't get dynamic config, use hardcoded fallback values
		logrus.WithError(err).Warn("Could not get dynamic config for client token, using defaults")
	}
	clientID := "d8a5ed958d274c2e8ee717e6a4b0971d"
	clientVersion := "1.2.86.152.gcb85a522"
	if config != nil {
		if config.ClientID != "" {
			clientID = config.ClientID
		}
		if config.ClientVersion != "" {
			clientVersion = config.ClientVersion
		}
	}

	payload := map[string]interface{}{
		"client_data": map[string]interface{}{
			"client_version": clientVersion,
			"client_id":      clientID,
			"js_sdk_data": map[string]interface{}{
				"device_brand": "Apple",
				"device_model": "unknown",
				"os":           "macos",
				"os_version":   "10.15",
				"device_id":    c.deviceID,
				"device_type":  "computer",
			},
		},
	}

	jsonBody, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "https://clienttoken.spotify.com/v1/clienttoken", bytes.NewReader(jsonBody))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:148.0) Gecko/20100101 Firefox/148.0")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://open.spotify.com/")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://open.spotify.com")
	req.Header.Set("Sec-GPC", "1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to get client token: received status %d", resp.StatusCode)
	}

	var data struct {
		GrantedToken struct {
			Token              string `json:"token"`
			ExpiryAfterSeconds int    `json:"expiry_after_seconds"`
		} `json:"granted_token"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("failed to decode client token response: %w", err)
	}

	c.clientToken = data.GrantedToken.Token
	c.clientTokenExp = time.Now().Add(time.Duration(data.GrantedToken.ExpiryAfterSeconds) * time.Second)

	if c.redis != nil {
		// Cache client token and its expiration
		c.redis.Set(c.ctx, "spoxy:client_token", c.clientToken, 0)
		c.redis.Set(c.ctx, "spoxy:client_token_exp", c.clientTokenExp.Format(time.RFC3339), 0)
	}

	return nil
}

func (c *Client) getServerTime() (int64, error) {
	resp, err := c.http.Get("https://open.spotify.com/api/server-time")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var data struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}
	return data.ServerTime, nil
}

func deobfuscateSecret(s string) string {
	// XOR logic from spot-secrets-go
	var cipherBytes []int
	for i, r := range s {
		cipherBytes = append(cipherBytes, int(r)^((i%33)+9))
	}

	// Concatenate numbers as string
	joined := ""
	for _, v := range cipherBytes {
		joined += fmt.Sprintf("%d", v)
	}

	// Hex round-trip (UTF-8 bytes to hex string then back to bytes is just the UTF-8 bytes)
	utf8Bytes := []byte(joined)

	// Base32 encode
	const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	encoder := base32.NewEncoding(base32Alphabet).WithPadding(base32.NoPadding)
	return encoder.EncodeToString(utf8Bytes)
}

func generateHOTP(secret string, counter int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		// Try custom alphabet if standard fails (though deobfuscateSecret uses custom)
		const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
		encoder := base32.NewEncoding(base32Alphabet).WithPadding(base32.NoPadding)
		key, err = encoder.DecodeString(secret)
		if err != nil {
			return "", err
		}
	}

	h := hmac.New(sha1.New, key)
	binary.Write(h, binary.BigEndian, counter)
	sum := h.Sum(nil)

	offset := sum[len(sum)-1] & 0xf
	v := int64(((int(sum[offset]) & 0x7f) << 24) |
		((int(sum[offset+1]) & 0xff) << 16) |
		((int(sum[offset+2]) & 0xff) << 8) |
		(int(sum[offset+3]) & 0xff))

	otp := v % 1000000
	return fmt.Sprintf("%06d", otp), nil
}

func (c *Client) ensureToken() error {
	if err := c.ensureClientToken(); err != nil {
		logrus.WithError(err).Warn("Failed to ensure client token, proceeding without it")
	}

	if c.token != "" && time.Now().Before(c.expiryTime) {
		return nil
	}

	// Try fetching from Redis cache first
	if c.redis != nil {
		if val, err := c.redis.Get(c.ctx, "spoxy:access_token").Result(); err == nil && val != "" {
			if exp, err := c.redis.Get(c.ctx, "spoxy:access_token_exp").Result(); err == nil && exp != "" {
				expTime, err := time.Parse(time.RFC3339, exp)
				if err == nil && time.Now().Before(expTime) {
					c.token = val
					c.expiryTime = expTime
					logrus.Debug("Using cached Spotify access token")
					return nil
				}
			}
		}
	}

	serverTime, err := c.getServerTime()
	if err != nil {
		logrus.WithError(err).Warn("Failed to get server time, using local time")
		serverTime = time.Now().Unix()
	}

	config, err := c.getDynamicConfig()
	if err != nil {
		return fmt.Errorf("failed to get dynamic config: %w", err)
	}

	// 2. Generate TOTP using dynamic secret
	rawSecret := config.TOTPSecret
	b32Secret := deobfuscateSecret(rawSecret)
	counter := serverTime / 30

	totpValue, err := generateHOTP(b32Secret, counter)
	if err != nil {
		return fmt.Errorf("failed to generate OTP: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"serverTime":    serverTime,
		"otp":           totpValue,
		"secret_prefix": b32Secret[:5],
		"version":       config.TOTPVersion,
	}).Debug("Generated dynamic TOTP")

	urlStr := fmt.Sprintf("https://open.spotify.com/api/token?reason=init&productType=web-player&totp=%s&totpServer=%d&totpVer=%d", totpValue, serverTime, config.TOTPVersion)
	req, _ := http.NewRequest("GET", urlStr, nil)

	// User-provided headers and cookies
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:148.0) Gecko/20100101 Firefox/148.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://open.spotify.com/")
	req.Header.Set("Origin", "https://open.spotify.com")
	req.Header.Set("Sec-GPC", "1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	// sp_t cookie matches device_id used in ensureClientToken
	req.Header.Set("Cookie", fmt.Sprintf("sp_t=%s; sp_landing=https%%3A%%2F%%2Fopen.spotify.com%%2F; sp_new=1", c.deviceID))

	if c.clientToken != "" {
		req.Header.Set("X-Client-Token", c.clientToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to get access token: received status %d", resp.StatusCode)
	}

	var data struct {
		AccessToken string `json:"accessToken"`
		ExpiryMs    int64  `json:"accessTokenExpirationTimestampMs"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("failed to decode access token response: %w", err)
	}

	c.token = data.AccessToken
	c.expiryTime = time.UnixMilli(data.ExpiryMs)

	if c.redis != nil {
		// Cache access token and its expiration
		c.redis.Set(c.ctx, "spoxy:access_token", c.token, 0)
		c.redis.Set(c.ctx, "spoxy:access_token_exp", c.expiryTime.Format(time.RFC3339), 0)
	}

	logrus.Info("Successfully obtained new Spotify access token and cached it")

	return nil
}

func parseLink(link string) (string, string, error) {
	m := linkRegex.FindStringSubmatch(link)
	if len(m) != 3 {
		return "", "", fmt.Errorf("invalid spotify link")
	}
	return m[1], m[2], nil
}

func (c *Client) get(urlStr string, v interface{}) error {
	return c.getInternal(urlStr, v, true)
}

func (c *Client) getInternal(urlStr string, v interface{}, retryOn401 bool) error {
	if err := c.ensureToken(); err != nil {
		return err
	}

	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:148.0) Gecko/20100101 Firefox/148.0")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://open.spotify.com/")
	req.Header.Set("Origin", "https://open.spotify.com")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("app-platform", "WebPlayer")
	appVersion := "1.2.58.261.g16b088e2"
	if cfg, err := c.getDynamicConfig(); err == nil && cfg.ClientVersion != "" {
		appVersion = cfg.ClientVersion
	}
	req.Header.Set("spotify-app-version", appVersion)

	// Consistent device_id/sp_t from client instance
	req.Header.Set("Cookie", fmt.Sprintf("sp_t=%s", c.deviceID))

	if c.clientToken != "" {
		req.Header.Set("client-token", c.clientToken)
		req.Header.Set("X-Client-Token", c.clientToken)
	}

	logrus.WithFields(logrus.Fields{
		"url":        urlStr,
		"has_at":     c.token != "",
		"has_ct":     c.clientToken != "",
		"at_first_8": func() string {
			if len(c.token) > 8 {
				return c.token[:8]
			}
			return ""
		}(),
	}).Debug("Making Spotify API request")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && retryOn401 {
		logrus.Warn("Spotify API returned 401 Unauthorized, clearing tokens and retrying...")
		c.token = ""
		c.clientToken = ""
		if c.redis != nil {
			c.redis.Del(c.ctx, "spoxy:access_token", "spoxy:access_token_exp", "spoxy:client_token", "spoxy:client_token_exp")
		}
		return c.getInternal(urlStr, v, false)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logrus.WithFields(logrus.Fields{
			"url":    urlStr,
			"status": resp.Status,
			"body":   string(bodyBytes),
		}).Error("Spotify API returned non-200 status")

		if resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("spotify api error: 429 Too Many Requests (rate limited)")
		}
		return fmt.Errorf("spotify api error: %s", resp.Status)
	}

	// Log first 1000 chars of body for debugging
	bodyStr := string(bodyBytes)
	if len(bodyStr) > 1000 {
		bodyStr = bodyStr[:1000] + "..."
	}
	logrus.WithField("body", bodyStr).Debug("Raw Spotify API response")

	if err := json.Unmarshal(bodyBytes, v); err != nil {
		logrus.WithFields(logrus.Fields{
			"url": urlStr,
			"err": err,
		}).Error("Failed to decode Spotify API response")
		return fmt.Errorf("failed to decode response from %s: %w", urlStr, err)
	}

	return nil
}

func (c *Client) graphqlRequest(operationName, variables, sha256Hash string, v interface{}) error {
	ext := fmt.Sprintf(`{"persistedQuery":{"version":1,"sha256Hash":"%s"}}`, sha256Hash)
	u, _ := url.Parse("https://api-partner.spotify.com/pathfinder/v1/query")
	q := u.Query()
	q.Set("operationName", operationName)
	q.Set("variables", variables)
	q.Set("extensions", ext)
	u.RawQuery = q.Encode()

	return c.get(u.String(), v)
}

func (c *Client) Track(id string) (*Track, error) {
	var data TrackResponse
	config, err := c.getDynamicConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic config: %w", err)
	}

	variables := fmt.Sprintf(`{"uri":"spotify:track:%s"}`, id)
	hash := config.GetTrackHash

	if err := c.graphqlRequest("getTrack", variables, hash, &data); err != nil {
		return nil, err
	}

	track := data.Data.TrackUnion
	if track.Typename == "NotFound" {
		return nil, fmt.Errorf("track not found: %s", id)
	}

	// Build artists
	artistObjs := make([]SimplifiedArtist, 0)

	// Add FirstArtist
	for _, item := range track.FirstArtist.Items {
		artistID := strings.TrimPrefix(item.URI, "spotify:artist:")
		artistObjs = append(artistObjs, SimplifiedArtist{
			ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/artist/" + artistID},
			Href:         "https://api.spotify.com/v1/artists/" + artistID,
			ID:           artistID,
			Name:         item.Profile.Name,
			Type:         "artist",
			URI:          item.URI,
		})
	}

	// Add OtherArtists
	for _, item := range track.OtherArtists.Items {
		artistID := strings.TrimPrefix(item.URI, "spotify:artist:")
		artistObjs = append(artistObjs, SimplifiedArtist{
			ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/artist/" + artistID},
			Href:         "https://api.spotify.com/v1/artists/" + artistID,
			ID:           artistID,
			Name:         item.Profile.Name,
			Type:         "artist",
			URI:          item.URI,
		})
	}

	// Build album
	album := track.AlbumOfTrack
	albumID := strings.TrimPrefix(album.URI, "spotify:album:")
	albumImages := make([]Image, 0, len(album.CoverArt.Sources))
	for _, s := range album.CoverArt.Sources {
		albumImages = append(albumImages, Image{URL: s.URL, Height: s.Height, Width: s.Width})
	}
	albumArtists := make([]SimplifiedArtist, 0, len(album.Artists.Items))
	for _, a := range album.Artists.Items {
		aID := strings.TrimPrefix(a.URI, "spotify:artist:")
		albumArtists = append(albumArtists, SimplifiedArtist{
			ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/artist/" + aID},
			Href:         "https://api.spotify.com/v1/artists/" + aID,
			ID:           aID,
			Name:         a.Profile.Name,
			Type:         "artist",
			URI:          a.URI,
		})
	}

	dateStr := ""
	if album.Date.IsoString != "" {
		// Example: "2010-01-01T00:00:00Z" -> "2010-01-01"
		if len(album.Date.IsoString) >= 10 {
			dateStr = album.Date.IsoString[:10]
		}
	}

	albumObj := SimplifiedAlbum{
		AlbumType:    "album",
		Artists:      albumArtists,
		ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/album/" + albumID},
		Href:         "https://api.spotify.com/v1/albums/" + albumID,
		ID:           albumID,
		Images:       albumImages,
		Name:         album.Name,
		ReleaseDate:  dateStr,
		Type:         "album",
		URI:          album.URI,
	}

	return &Track{
		Album:        albumObj,
		Artists:      artistObjs,
		DurationMs:   track.Duration.TotalMilliseconds,
		ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/track/" + id},
		Href:         "https://api.spotify.com/v1/tracks/" + id,
		ID:           id,
		IsPlayable:   track.Playability.Playable,
		Name:         track.Name,
		Type:         "track",
		URI:          "spotify:track:" + id,
	}, nil
}

func (c *Client) Artist(id string) ([]Track, error) {
	config, err := c.getDynamicConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic config: %w", err)
	}

	var data ArtistResponse
	variables := fmt.Sprintf(`{"uri":"spotify:artist:%s","locale":"","includePrerelease":false}`, id)
	hash := config.GetArtistHash

	if err := c.graphqlRequest("queryArtistOverview", variables, hash, &data); err != nil {
		return nil, err
	}

	artist := data.Data.ArtistUnion
	if artist.Typename == "NotFound" {
		return nil, fmt.Errorf("artist not found: %s", id)
	}

	var tracks []Track
	for _, item := range artist.Discography.TopTracks.Items {
		trackID := item.Track.ID
		if trackID == "" {
			trackID = strings.TrimPrefix(item.Track.URI, "spotify:track:")
		}
		t, err := c.Track(trackID)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"artist_id": id,
				"track_id":  trackID,
				"err":       err,
			}).Warn("Failed to fetch track details for artist")
			continue
		}
		tracks = append(tracks, *t)
	}

	return tracks, nil
}

func (c *Client) Album(id string) ([]Track, error) {
	config, err := c.getDynamicConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic config: %w", err)
	}

	var data AlbumResponse
	variables := fmt.Sprintf(`{"uri":"spotify:album:%s","locale":"","offset":0,"limit":50}`, id)
	hash := config.GetAlbumHash

	if err := c.graphqlRequest("getAlbum", variables, hash, &data); err != nil {
		return nil, err
	}

	album := data.Data.AlbumUnion
	if album.Typename == "NotFound" {
		return nil, fmt.Errorf("album not found: %s", id)
	}

	var tracks []Track
	for _, item := range album.TracksV2.Items {
		trackID := strings.TrimPrefix(item.Track.URI, "spotify:track:")
		t, err := c.Track(trackID)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"album_id": id,
				"track_id": trackID,
				"err":      err,
			}).Warn("Failed to fetch track details for album")
			continue
		}
		tracks = append(tracks, *t)
	}

	return tracks, nil
}

func (c *Client) Playlist(id string) ([]Track, error) {
	var tracks []Track
	offset := 0
	limit := 100

	config, err := c.getDynamicConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic config: %w", err)
	}

	for {
		var data PlaylistResponse

		variables := fmt.Sprintf(`{"uri":"spotify:playlist:%s","offset":%d,"limit":%d,"enableWatchFeedEntrypoint":false}`, id, offset, limit)
		hash := config.FetchPlaylistHash

		if err := c.graphqlRequest("fetchPlaylist", variables, hash, &data); err != nil {
			return nil, err
		}

		items := data.Data.PlaylistV2.Content.Items
		if len(items) == 0 {
			break
		}

		for _, item := range items {
			d := item.ItemV2.Data
			if d.Typename != "Track" {
				continue
			}

			trackID := d.ID
			if trackID == "" && strings.HasPrefix(d.URI, "spotify:track:") {
				trackID = strings.TrimPrefix(d.URI, "spotify:track:")
			}

			// Build artists
			artistObjs := make([]SimplifiedArtist, 0, len(d.Artists.Items))
			for _, a := range d.Artists.Items {
				aID := strings.TrimPrefix(a.URI, "spotify:artist:")
				artistObjs = append(artistObjs, SimplifiedArtist{
					ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/artist/" + aID},
					Href:         "https://api.spotify.com/v1/artists/" + aID,
					ID:           aID,
					Name:         a.Profile.Name,
					Type:         "artist",
					URI:          a.URI,
				})
			}

			// Build album
			album := d.AlbumOfTrack
			albumID := strings.TrimPrefix(album.URI, "spotify:album:")
			albumImages := make([]Image, 0, len(album.CoverArt.Sources))
			for _, s := range album.CoverArt.Sources {
				albumImages = append(albumImages, Image{URL: s.URL, Height: s.Height, Width: s.Width})
			}
			albumArtists := make([]SimplifiedArtist, 0, len(album.Artists.Items))
			for _, a := range album.Artists.Items {
				aID := strings.TrimPrefix(a.URI, "spotify:artist:")
				albumArtists = append(albumArtists, SimplifiedArtist{
					ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/artist/" + aID},
					Href:         "https://api.spotify.com/v1/artists/" + aID,
					ID:           aID,
					Name:         a.Profile.Name,
					Type:         "artist",
					URI:          a.URI,
				})
			}
			albumObj := SimplifiedAlbum{
				AlbumType:    "album",
				Artists:      albumArtists,
				ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/album/" + albumID},
				Href:         "https://api.spotify.com/v1/albums/" + albumID,
				ID:           albumID,
				Images:       albumImages,
				Name:         album.Name,
				Type:         "album",
				URI:          album.URI,
			}

			tracks = append(tracks, Track{
				Album:        albumObj,
				Artists:      artistObjs,
				DurationMs:   d.TrackDuration.TotalMilliseconds,
				ExternalURLs: ExternalURLs{Spotify: "https://open.spotify.com/track/" + trackID},
				Href:         "https://api.spotify.com/v1/tracks/" + trackID,
				ID:           trackID,
				Name:         d.Name,
				Type:         "track",
				URI:          d.URI,
			})
		}

		offset += limit
		if offset >= data.Data.PlaylistV2.Content.TotalCount || len(tracks) >= data.Data.PlaylistV2.Content.TotalCount {
			break
		}
	}

	return tracks, nil
}

func (c *Client) Resolve(link string) ([]Track, error) {
	kind, id, err := parseLink(link)
	if err != nil {
		return nil, err
	}

	cacheKey := fmt.Sprintf("spoxy:%s:%s", kind, id)
	ctx := context.Background()

	if c.redis != nil {
		if val, err := c.redis.Get(ctx, cacheKey).Result(); err == nil {
			var cachedTracks []Track
			if err := json.Unmarshal([]byte(val), &cachedTracks); err == nil {
				return cachedTracks, nil
			}
		}
	}

	var tracks []Track
	switch kind {
	case "track":
		t, err := c.Track(id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				tracks = []Track{}
			} else {
				return nil, err
			}
		} else {
			tracks = []Track{*t}
		}
	case "album":
		albumTracks, err := c.Album(id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				tracks = []Track{}
			} else {
				return nil, err
			}
		} else {
			tracks = albumTracks
		}
	case "artist":
		artistTracks, err := c.Artist(id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				tracks = []Track{}
			} else {
				return nil, err
			}
		} else {
			tracks = artistTracks
		}
	case "playlist":
		playlistTracks, err := c.Playlist(id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				tracks = []Track{}
			} else {
				return nil, err
			}
		} else {
			tracks = playlistTracks
		}
	default:
		return nil, fmt.Errorf("unsupported link type: %s", kind)
	}

	if c.redis != nil {
		if data, err := json.Marshal(tracks); err == nil {
			c.redis.Set(ctx, cacheKey, data, 24*time.Hour)
		}
	}

	return tracks, nil
}
