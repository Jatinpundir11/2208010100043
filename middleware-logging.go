package middleware

import (
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

// responseWriter wrapper to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// LoggingMiddleware logs each request with method, URI, status, and duration
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// wrap response writer
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// call next handler
		next.ServeHTTP(rw, r)

		duration := time.Since(start)

		logrus.WithFields(logrus.Fields{
			"method":   r.Method,
			"path":     r.RequestURI,
			"status":   rw.statusCode,
			"duration": duration,
			"client":   r.RemoteAddr,
		}).Info("incoming request")
	})
}