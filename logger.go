package main

import (
	"log"
	"net/http"
	"os"
)

var reqlog = log.New(os.Stderr, "Served request: ", 0)

func requestLogger(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Printf("%s %s", r.Method, r.URL.Path)
		})
	}
}
