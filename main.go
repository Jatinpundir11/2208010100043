package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	"url-shortener/middleware"
)

const (
	DefaultValidityMinutes = 30
	CodeLength             = 6
)

var base62 = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

type Link struct {
	LongURL   string    json:"long_url"
	ShortCode string    json:"short_code"
	CreatedAt time.Time json:"created_at"
	ExpiresAt time.Time json:"expires_at"
	Clicks    int64     json:"clicks"
}

type Store struct {
	sync.RWMutex
	data   map[string]*Link
	domain string // e.g. http://localhost:8080
}

func NewStore(domain string) *Store {
	return &Store{
		data:   make(map[string]*Link),
		domain: domain,
	}
}

func (s *Store) Create(longURL string, custom string, validity time.Duration) (*Link, error) {
	s.Lock()
	defer s.Unlock()

	// validate URL
	_, err := url.ParseRequestURI(longURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url")
	}

	var code string
	if custom != "" {
		if _, exists := s.data[custom]; exists {
			return nil, fmt.Errorf("custom code already exists")
		}
		code = custom
	} else {
		// generate unique code
		for {
			code = generateCode(CodeLength)
			if _, exists := s.data[code]; !exists {
				break
			}
		}
	}

	now := time.Now().UTC()
	l := &Link{
		LongURL:   longURL,
		ShortCode: code,
		CreatedAt: now,
		ExpiresAt: now.Add(validity),
		Clicks:    0,
	}
	s.data[code] = l
	logrus.WithFields(logrus.Fields{
		"action":     "create",
		"short_code": code,
		"long_url":   longURL,
		"expires_at": l.ExpiresAt,
	}).Info("link created")
	return l, nil
}

func (s *Store) Get(code string) (*Link, bool) {
	s.RLock()
	defer s.RUnlock()
	l, ok := s.data[code]
	return l, ok
}

func (s *Store) Increment(code string) {
	s.Lock()
	defer s.Unlock()
	if l, ok := s.data[code]; ok {
		l.Clicks++
	}
}

func (s *Store) CleanupExpired() {
	for {
		time.Sleep(1 * time.Minute)
		now := time.Now().UTC()
		s.Lock()
		for k, v := range s.data {
			if now.After(v.ExpiresAt) {
				delete(s.data, k)
				logrus.WithField("short_code", k).Info("expired and removed")
			}
		}
		s.Unlock()
	}
}

func generateCode(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = base62[rand.Intn(len(base62))]
	}
	return string(b)
}

/* --- HTTP Handlers --- */

type ShortenRequest struct {
	URL            string json:"url"
	CustomCode     string json:"custom_code,omitempty"
	ValidityMinute int    json:"validity_minutes,omitempty"
}

type ShortenResponse struct {
	ShortURL  string    json:"short_url"
	ShortCode string    json:"short_code"
	ExpiresAt time.Time json:"expires_at"
	LongURL   string    json:"long_url"
}

func shortenHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ShortenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.URL == "" {
			httpError(w, http.StatusBadRequest, "url is required")
			return
		}
		validity := time.Duration(DefaultValidityMinutes) * time.Minute
		if req.ValidityMinute > 0 {
			validity = time.Duration(req.ValidityMinute) * time.Minute
		}
		link, err := store.Create(req.URL, req.CustomCode, validity)
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp := ShortenResponse{
			ShortURL:  fmt.Sprintf("%s/%s", store.domain, link.ShortCode),
			ShortCode: link.ShortCode,
			ExpiresAt: link.ExpiresAt,
			LongURL:   link.LongURL,
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

func redirectHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		code := vars["code"]
		link, ok := store.Get(code)
		if !ok {
			httpError(w, http.StatusNotFound, "short link not found")
			return
		}
		if time.Now().UTC().After(link.ExpiresAt) {
			httpError(w, http.StatusGone, "short link expired")
			return
		}
		store.Increment(code)
		logrus.WithFields(logrus.Fields{
			"action":     "redirect",
			"short_code": code,
			"to":         link.LongURL,
		}).Info("redirecting")
		http.Redirect(w, r, link.LongURL, http.StatusFound)
	}
}

func statsHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		code := vars["code"]
		link, ok := store.Get(code)
		if !ok {
			httpError(w, http.StatusNotFound, "short link not found")
			return
		}
		writeJSON(w, http.StatusOK, link)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

/* --- helpers --- */

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	rand.Seed(time.Now().UnixNano())
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	domain := "http://localhost:8080" // change if deploying
	store := NewStore(domain)
	go store.CleanupExpired()

	r := mux.NewRouter()

	// 👇 Apply logging middleware globally
	r.Use(middleware.LoggingMiddleware)

	api := r.PathPrefix("/api").Subrouter()
	api.HandleFunc("/shorten", shortenHandler(store)).Methods("POST")
	api.HandleFunc("/stats/{code}", statsHandler(store)).Methods("GET")
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/{code}", redirectHandler(store)).Methods("GET")

	srv := &http.Server{
		Handler:      r,
		Addr:         ":8080",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	logrus.Infof("starting server on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		logrus.Fatal(err)
	}
}