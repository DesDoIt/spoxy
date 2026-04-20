package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/DesDoIt/spoxy/spotify"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
)

func main() {
	// Configure logrus
	log.SetFormatter(&log.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)

	proxyURL := os.Getenv("SPOXY_PROXY_URL")
	redisURL := os.Getenv("SPOXY_REDIS_URL")

	var rdb *redis.Client
	if redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.WithField("url", redisURL).Fatalf("Invalid REDIS_URL: %v", err)
		}
		rdb = redis.NewClient(opt)
	}

	client := spotify.NewClient(proxyURL, rdb)

	http.HandleFunc("/api/resolve", func(w http.ResponseWriter, r *http.Request) {
		link := r.URL.Query().Get("link")
		if link == "" {
			http.Error(w, "query param 'link' is required", http.StatusBadRequest)
			return
		}

		log.WithField("link", link).Info("Resolving track metadata")
		res, err := client.Resolve(r.Context(), link)
		if err != nil {
			log.WithFields(log.Fields{
				"link":  link,
				"error": err,
			}).Error("Error resolving link")

			status := http.StatusInternalServerError
			if err.Error() == spotify.ERROR_UNSUPPORTED {
				status = http.StatusBadRequest
			} else if err.Error() == spotify.ERROR_NO_TRACK_FOUND {
				status = http.StatusNotFound
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if len(res.Tracks) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": spotify.ERROR_NO_TRACK_FOUND})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(res); err != nil {
			log.WithError(err).Error("Error encoding response")
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.WithField("port", port).Info("Starting server")
	handler := gzipMiddleware(http.DefaultServeMux)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.WithError(err).Fatal("Server failed to start")
	}
}

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w gzipResponseWriter) Flush() {
	if f, ok := w.Writer.(*gzip.Writer); ok {
		f.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		defer gz.Close()

		next.ServeHTTP(gzipResponseWriter{Writer: gz, ResponseWriter: w}, r)
	})
}
