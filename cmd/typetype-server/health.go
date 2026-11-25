package main

import (
	"net/http"
	"os"
	"time"
)

// healthCheck is the container healthcheck ("typetype-server health"): the image has no shell or wget, so the binary
// asks its own /health and exits 0 only on a 200.
func healthCheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
