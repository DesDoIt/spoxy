package main

import (
	"encoding/json"
	"net/http"
	"os"

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
		res, err := client.Resolve(link)
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
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.WithError(err).Fatal("Server failed to start")
	}
}
