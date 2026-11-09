// Package wall serves the browser camera wall: one page that lays every feed
// out with the layout solver, and a single multiplexed stream of JPEG frames.
package wall

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vinodhalaharvi/camwall/feeds"
	"github.com/vinodhalaharvi/camwall/layout"
	"github.com/vinodhalaharvi/camwall/record"
)

//go:embed web
var webFS embed.FS

// Server is the browser wall.
type Server struct {
	Title  string
	Feeds  []feeds.Feed
	Hubs   []*Hub // nil for feeds served as files
	Tools  feeds.Tools
	Rec    *record.Recorder // nil when sound is not being recorded
	RecDir string           // recordings to list (even when another camwall records them)

	listeners chan struct{} // limits concurrent live-sound streams

	mu       sync.Mutex
	cache    map[string]*layout.Solved
	order    []string
	inflight map[string]*solveCall
	solving  chan struct{}  // one solve at a time
	procs    sync.WaitGroup // live-sound FFmpeg processes
}

type solveCall struct {
	done chan struct{}
	sv   *layout.Solved
	err  error
}

// Static reports whether a feed goes straight to the browser as a file: a
// local picture or animation the browser shows (and animates) itself, or a
// JPEG copy of a picture it cannot show (feeds.Prepare makes it).
func Static(f feeds.Feed) bool {
	return f.Browser != ""
}

// New makes a server and a hub for every moving feed.
func New(title string, t feeds.Tools, fs []feeds.Feed, fps, maxSide, quality int) *Server {
	s := &Server{Title: title, Feeds: fs, Hubs: make([]*Hub, len(fs)), Tools: t, cache: map[string]*layout.Solved{},
		inflight: map[string]*solveCall{}, solving: make(chan struct{}, 1), listeners: make(chan struct{}, 8)}
	for i, f := range fs {
		if !Static(f) {
			s.Hubs[i] = &Hub{ID: i, Feed: f, Tools: t, FPS: fps, MaxSide: maxSide, Quality: quality, Linger: 15 * time.Second}
		}
	}
	return s
}

// Solve returns the solved wall for the shown feeds (ids) with the given
// weights (one per feed on the server), solving each combination once.
// Solves run one at a time and outside the lock, so a slow one never holds
// up requests for walls already solved; concurrent requests for the same
// wall share one solve. Tiles of the result refer to positions in ids.
func (s *Server) Solve(ids []int, weights []float64) (*layout.Solved, error) {
	key := fmt.Sprint(ids, weights)
	s.mu.Lock()
	if sv, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return sv, nil
	}
	if c, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		<-c.done
		return c.sv, c.err
	}
	c := &solveCall{done: make(chan struct{})}
	s.inflight[key] = c
	s.mu.Unlock()

	lf := make([]layout.Feed, len(ids))
	for i, id := range ids {
		lf[i] = layout.Feed{Aspect: s.Feeds[id].Aspect, Weight: weights[id]}
	}
	s.solving <- struct{}{}
	c.sv, c.err = layout.Solve(lf)
	<-s.solving

	s.mu.Lock()
	delete(s.inflight, key)
	if c.err == nil {
		s.cache[key] = c.sv
		s.order = append(s.order, key)
		if len(s.order) > 32 {
			delete(s.cache, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.mu.Unlock()
	close(c.done)
	return c.sv, c.err
}

// Close stops every feed's FFmpeg and waits for them (and any live-sound
// FFmpeg) to exit.
func (s *Server) Close() {
	var wg sync.WaitGroup
	for _, h := range s.Hubs {
		if h != nil {
			wg.Add(1)
			go func(h *Hub) { defer wg.Done(); h.Stop() }(h)
		}
	}
	wg.Wait()
	s.procs.Wait()
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
	mux.HandleFunc("GET /audio/{id}", s.audio)
	mux.HandleFunc("GET /recordings", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, sub, "recordings.html")
	})
	mux.HandleFunc("GET /api/recordings", s.apiRecordings)
	mux.HandleFunc("GET /rec/{folder}/{file}", s.recording)
	return mux
}

type feedInfo struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	Aspect float64 `json:"aspect"`
	Weight float64 `json:"weight"`
	Static bool    `json:"static"`
	Audio  bool    `json:"audio"`
	Note   string  `json:"note,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) apiFeeds(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Title      string     `json:"title"`
		Feeds      []feedInfo `json:"feeds"`
		Recordings bool       `json:"recordings"`
	}{Title: s.Title, Recordings: s.RecDir != ""}
	for i, f := range s.Feeds {
		out.Feeds = append(out.Feeds, feedInfo{i, f.Name, string(f.Kind), f.Aspect, f.Weight, Static(f), f.Audio && !Static(f), f.Note})
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
	ids := make([]int, 0, len(s.Feeds))
	if q := r.URL.Query().Get("show"); q != "" {
		seen := map[int]bool{}
		for _, p := range strings.Split(q, ",") {
			id, err := strconv.Atoi(p)
			if err != nil || id < 0 || id >= len(s.Feeds) || seen[id] {
				http.Error(w, "show: feed numbers from 0, each once", http.StatusBadRequest)
				return
			}
			seen[id] = true
			ids = append(ids, id)
		}
		sort.Ints(ids)
	} else {
		for i := range s.Feeds {
			ids = append(ids, i)
		}
	}
	if len(ids) == 0 {
		writeJSON(w, struct {
			Tiles []layout.Tile `json:"tiles"`
		}{[]layout.Tile{}})
		return
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
			// half steps from ½ to 10, so a few slider moves can't make
			// dozens of distinct sizes
			weights[i] = math.Min(10, math.Max(0.5, math.Round(x*2)/2))
		}
	}
	sv, err := s.Solve(ids, weights)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	wall := sv.Fit(aspect)
	for i := range wall.Tiles {
		wall.Tiles[i].Feed = ids[wall.Tiles[i].Feed]
	}
	writeJSON(w, struct {
		Tiles    []layout.Tile `json:"tiles"`
		Smallest float64       `json:"smallest"`
		Fill     float64       `json:"fill"`
		Shapes   float64       `json:"shapes"`
		Evened   bool          `json:"evened,omitempty"` // sizes too varied; laid out equal
	}{wall.Tiles, wall.Smallest, wall.Fill, sv.Count(), sv.Evened})
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
	http.ServeFile(w, r, s.Feeds[id].Browser)
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

// Sound for live listening: 24 kHz mono 16-bit little-endian PCM, which the
// page plays with Web Audio as it arrives (well under a second behind).
const (
	AudioRate     = 24000
	audioChunk    = AudioRate * 2 / 10 // 100 ms
	maxListenTime = 4 * time.Hour
)

// audio streams a feed's sound while the request lasts. Each listener gets
// its own FFmpeg; at most a few run at once.
func (s *Server) audio(w http.ResponseWriter, r *http.Request) {
	id, ok := s.feedID(w, r)
	if !ok {
		return
	}
	f := s.Feeds[id]
	if !f.Audio || Static(f) {
		http.Error(w, "this feed has no sound", http.StatusNotFound)
		return
	}
	select {
	case s.listeners <- struct{}{}:
		defer func() { <-s.listeners }()
	default:
		http.Error(w, "too many people listening", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxListenTime)
	defer cancel()
	octx, ocancel := context.WithTimeout(ctx, 30*time.Second)
	g, err := f.Opened(octx, s.Tools)
	ocancel()
	if err != nil {
		http.Error(w, feeds.Redact(err.Error()), http.StatusBadGateway)
		return
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-fflags", "nobuffer", "-flags", "low_delay"}
	args = append(args, g.InputArgs(10)...)
	args = append(args, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", strconv.Itoa(AudioRate), "-f", "s16le", "pipe:1")
	cmd := feeds.Child(exec.CommandContext(ctx, s.Tools.FFmpeg, args...))
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.procs.Add(1)
	defer s.procs.Done()
	defer cmd.Wait()
	defer cancel()
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Audio-Format", "s16le;rate="+strconv.Itoa(AudioRate)+";channels=1")
	fl, _ := w.(http.Flusher)
	buf := make([]byte, audioChunk)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// apiRecordings lists the recorded sound, per feed, newest first.
func (s *Server) apiRecordings(w http.ResponseWriter, r *http.Request) {
	if s.RecDir == "" {
		http.Error(w, "sound is not being recorded (start camwall with -record DIR)", http.StatusNotFound)
		return
	}
	folders, err := record.List(s.RecDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status := map[string]string{}
	if s.Rec != nil {
		for i, st := range s.Rec.Status() {
			status[record.FolderName(i, s.Feeds[i].Name)] = st
		}
	}
	type folder struct {
		record.Folder
		Status string `json:"status,omitempty"`
	}
	out := struct {
		Segment float64  `json:"segment"` // seconds per file, when this camwall records
		Now     string   `json:"now"`     // the time on this computer's clock
		Zone    string   `json:"zone"`
		Folders []folder `json:"folders"`
	}{Folders: make([]folder, 0, len(folders))}
	now := time.Now()
	out.Now, out.Zone = now.Format("2006-01-02T15:04:05"), now.Format("MST")
	if s.Rec != nil {
		out.Segment = s.Rec.Segment.Seconds()
	}
	for _, f := range folders {
		out.Folders = append(out.Folders, folder{f, status[f.Name]})
	}
	writeJSON(w, out)
}

// recording serves one recorded file (with seeking).
func (s *Server) recording(w http.ResponseWriter, r *http.Request) {
	if s.RecDir == "" {
		http.NotFound(w, r)
		return
	}
	p, ok := record.Path(s.RecDir, r.PathValue("folder"), r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/mp4")
	http.ServeFile(w, r, p)
}
