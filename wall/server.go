// Package wall serves the browser camera wall: one page that lays every feed
// out with the layout solver, and a single multiplexed stream of JPEG frames.
package wall

import (
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vinodhalaharvi/camwall/feeds"
	"github.com/vinodhalaharvi/camwall/layout"
)

//go:embed web
var webFS embed.FS

// Server is the browser wall.
type Server struct {
	Title string
	Feeds []feeds.Feed
	Hubs  []*Hub // nil for feeds served as files

	mu    sync.Mutex
	cache map[string]*layout.Solved
	order []string
}

// Static reports whether a feed can go straight to the browser as a file:
// a local picture or GIF, which the browser shows (and animates) itself.
func Static(f feeds.Feed) bool {
	return (f.Kind == feeds.Image || f.Kind == feeds.GIF) && feeds.Scheme(f.Src) == ""
}

// New makes a server and a hub for every moving feed.
func New(title, ffmpeg string, fs []feeds.Feed, fps, maxSide, quality int) *Server {
	s := &Server{Title: title, Feeds: fs, Hubs: make([]*Hub, len(fs)), cache: map[string]*layout.Solved{}}
	for i, f := range fs {
		if !Static(f) {
			s.Hubs[i] = &Hub{ID: i, Feed: f, FFmpeg: ffmpeg, FPS: fps, MaxSide: maxSide, Quality: quality, Linger: 15 * time.Second}
		}
	}
	return s
}

// Solve returns the solved wall for a set of weights, solving it once.
func (s *Server) Solve(weights []float64) (*layout.Solved, error) {
	key := fmt.Sprint(weights)
	s.mu.Lock()
	defer s.mu.Unlock()
	if sv, ok := s.cache[key]; ok {
		return sv, nil
	}
	lf := make([]layout.Feed, len(s.Feeds))
	for i, f := range s.Feeds {
		lf[i] = layout.Feed{Aspect: f.Aspect, Weight: weights[i]}
	}
	sv, err := layout.Solve(lf)
	if err != nil {
		return nil, err
	}
	s.cache[key] = sv
	s.order = append(s.order, key)
	if len(s.order) > 32 {
		delete(s.cache, s.order[0])
		s.order = s.order[1:]
	}
	return sv, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/feeds", s.apiFeeds)
	mux.HandleFunc("GET /api/layout", s.apiLayout)
	mux.HandleFunc("GET /api/status", s.apiStatus)
	mux.HandleFunc("GET /media/{id}", s.media)
	mux.HandleFunc("GET /snapshot/{id}", s.snapshot)
	mux.HandleFunc("GET /stream", s.stream)
	return mux
}

type feedInfo struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	Aspect float64 `json:"aspect"`
	Weight float64 `json:"weight"`
	Static bool    `json:"static"`
	Note   string  `json:"note,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) apiFeeds(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Title string     `json:"title"`
		Feeds []feedInfo `json:"feeds"`
	}{Title: s.Title}
	for i, f := range s.Feeds {
		out.Feeds = append(out.Feeds, feedInfo{i, f.Name, string(f.Kind), f.Aspect, f.Weight, Static(f), f.Note})
	}
	writeJSON(w, out)
}

func (s *Server) apiLayout(w http.ResponseWriter, r *http.Request) {
	aspect, err := strconv.ParseFloat(r.URL.Query().Get("aspect"), 64)
	if err != nil || !(aspect > 0.05 && aspect < 20) {
		http.Error(w, "aspect: want width/height of the screen", http.StatusBadRequest)
		return
	}
	weights := make([]float64, len(s.Feeds))
	for i, f := range s.Feeds {
		weights[i] = f.Weight
	}
	if q := r.URL.Query().Get("weights"); q != "" {
		parts := strings.Split(q, ",")
		if len(parts) != len(weights) {
			http.Error(w, "weights: one per feed", http.StatusBadRequest)
			return
		}
		for i, p := range parts {
			x, err := strconv.ParseFloat(p, 64)
			if err != nil || !(x > 0) || x > 100 {
				http.Error(w, "weights: positive numbers", http.StatusBadRequest)
				return
			}
			weights[i] = math.Round(x*100) / 100
		}
	}
	sv, err := s.Solve(weights)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	wall := sv.Fit(aspect)
	writeJSON(w, struct {
		Tiles    []layout.Tile `json:"tiles"`
		Smallest float64       `json:"smallest"`
		Fill     float64       `json:"fill"`
		Shapes   float64       `json:"shapes"`
	}{wall.Tiles, wall.Smallest, wall.Fill, sv.Count()})
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	out := make([]Status, len(s.Feeds))
	for i, h := range s.Hubs {
		if h == nil {
			out[i] = Status{"live", 0}
		} else {
			out[i] = h.Status()
		}
	}
	writeJSON(w, out)
}

func (s *Server) feedID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("id"), ".jpg"))
	if err != nil || id < 0 || id >= len(s.Feeds) {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

// media serves a local picture or GIF as-is.
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	id, ok := s.feedID(w, r)
	if !ok {
		return
	}
	if !Static(s.Feeds[id]) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, s.Feeds[id].Src)
}

// snapshot is the latest frame of a moving feed as a JPEG.
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.feedID(w, r)
	if !ok {
		return
	}
	h := s.Hubs[id]
	if h == nil {
		s.media(w, r)
		return
	}
	ch := make(chan struct{}, 1)
	leave := h.Subscribe(ch)
	defer leave()
	frame, _ := h.Latest()
	if frame == nil {
		select {
		case <-ch:
			frame, _ = h.Latest()
		case <-time.After(15 * time.Second):
		case <-r.Context().Done():
			return
		}
	}
	if frame == nil {
		http.Error(w, "no picture from this feed yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(frame)
}

// stream sends frames of the requested feeds over one response, so a wall
// with many feeds needs one connection (browsers allow only ~6 per server).
// Each record is a 2-byte feed id, a 4-byte length, then the JPEG; big-endian.
// A slow viewer skips frames rather than falling behind.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	var ids []int
	for _, p := range strings.Split(r.URL.Query().Get("ids"), ",") {
		id, err := strconv.Atoi(p)
		if err == nil && id >= 0 && id < len(s.Hubs) && s.Hubs[id] != nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		http.Error(w, "ids: no moving feeds requested", http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan struct{}, 1)
	for _, id := range ids {
		defer s.Hubs[id].Subscribe(ch)()
	}
	sent := make(map[int]uint64, len(ids))
	var hdr [6]byte
	ping := time.NewTicker(10 * time.Second)
	defer ping.Stop()
	ch <- struct{}{} // send whatever frames exist right away
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			// an empty record (id 0xFFFF) keeps proxies and idle timers awake
			binary.BigEndian.PutUint16(hdr[0:], 0xFFFF)
			binary.BigEndian.PutUint32(hdr[2:], 0)
			rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := w.Write(hdr[:]); err != nil {
				return
			}
			fl.Flush()
		case <-ch:
			for _, id := range ids {
				frame, seq := s.Hubs[id].Latest()
				if frame == nil || sent[id] == seq {
					continue
				}
				sent[id] = seq
				binary.BigEndian.PutUint16(hdr[0:], uint16(id))
				binary.BigEndian.PutUint32(hdr[2:], uint32(len(frame)))
				rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if _, err := w.Write(hdr[:]); err != nil {
					return
				}
				if _, err := w.Write(frame); err != nil {
					return
				}
			}
			fl.Flush()
		}
	}
}
