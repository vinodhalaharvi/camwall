// Package record keeps the sound of every feed that has a microphone, so it
// can be listened to later.
//
// Each feed gets its own folder, and its sound is written in segments aligned
// to the clock (14:00, 14:15, …) and named by the time they start, e.g.
// "01 Front door/2026-11-08_14-15-00.m4a". The files are fragmented MP4, so
// a segment cut short by a power cut or a crash still plays up to its last
// second. Old segments are
// deleted after the retention period.
package record

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vinodhalaharvi/camwall/feeds"
)

// Recorder records the sound of feeds.
type Recorder struct {
	Dir     string
	Tools   feeds.Tools
	Feeds   []feeds.Feed
	Segment time.Duration // length of each file (default 15 minutes)
	Keep    time.Duration // delete files older than this; 0 keeps everything
	Bitrate string        // AAC bitrate (default 48k, mono)

	mu     sync.Mutex
	status map[int]string
}

// Wants reports whether a feed's sound should be recorded: it has sound, or
// it is a live feed whose sound could not be checked at start-up (an
// offline camera). Pictures and animations have none.
func Wants(f feeds.Feed) bool {
	if f.Audio {
		return true
	}
	return f.Kind == feeds.Live && f.Note != ""
}

var unsafe = regexp.MustCompile(`[^\p{L}\p{N} ._()&'+,-]+`)

// FolderName is the folder for feed i: its number, so names never collide,
// then its name made safe for any file system.
func FolderName(i int, name string) string {
	n := strings.TrimSpace(unsafe.ReplaceAllString(name, "_"))
	n = strings.Trim(n, ". ")
	if len(n) > 60 {
		n = n[:60]
	}
	return fmt.Sprintf("%02d %s", i+1, n)
}

// Run records until ctx ends. It returns once every recorder has stopped.
func (r *Recorder) Run(ctx context.Context) {
	if r.Segment <= 0 {
		r.Segment = 15 * time.Minute
	}
	if r.Bitrate == "" {
		r.Bitrate = "48k"
	}
	r.status = map[int]string{}
	var wg sync.WaitGroup
	for i, f := range r.Feeds {
		if !Wants(f) {
			continue
		}
		dir := filepath.Join(r.Dir, FolderName(i, f.Name))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("record: %v", err)
			continue
		}
		wg.Add(1)
		go func(i int, f feeds.Feed, dir string) {
			defer wg.Done()
			r.loop(ctx, i, f, dir)
		}(i, f, dir)
	}
	if r.Keep > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				r.Prune(time.Now())
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Minute):
				}
			}
		}()
	}
	wg.Wait()
}

// Status says, per feed number, whether its sound is being recorded.
func (r *Recorder) Status() map[int]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int]string, len(r.status))
	for k, v := range r.status {
		out[k] = v
	}
	return out
}

func (r *Recorder) set(i int, s string) {
	r.mu.Lock()
	r.status[i] = s
	r.mu.Unlock()
}

// Args is the FFmpeg command line that records a feed's sound into dir.
func (r *Recorder) Args(f feeds.Feed, dir string) []string {
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	a = append(a, f.InputArgs(10)...)
	return append(a, "-map", "0:a:0", "-vn", "-ac", "1", "-c:a", "aac", "-b:a", r.Bitrate,
		"-f", "segment", "-segment_time", strconv.Itoa(int(r.Segment.Seconds())), "-segment_atclocktime", "1",
		"-reset_timestamps", "1", "-strftime", "1",
		// one-second fragments, flushed as written: a crash loses at most a second
		"-segment_format", "mp4", "-segment_format_options", "movflags=+frag_keyframe+empty_moov+default_base_moof:frag_duration=1000000:flush_packets=1",
		filepath.Join(dir, "%Y-%m-%d_%H-%M-%S.m4a"))
}

func (r *Recorder) loop(ctx context.Context, i int, f feeds.Feed, dir string) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		octx, cancel := context.WithTimeout(ctx, 30*time.Second)
		g, err := f.Opened(octx, r.Tools)
		cancel()
		if err == nil {
			r.set(i, "recording")
			cmd := exec.CommandContext(ctx, r.Tools.FFmpeg, r.Args(g, dir)...)
			var tail strings.Builder
			cmd.Stderr = &limited{b: &tail, n: 2048}
			err = cmd.Run()
			if ctx.Err() != nil {
				return
			}
			if msg := strings.TrimSpace(tail.String()); msg != "" {
				err = fmt.Errorf("%s", lastLine(msg))
			}
			if strings.Contains(tail.String(), "matches no streams") {
				log.Printf("record: feed %d (%s) has no sound; not recording it", i+1, f.Name)
				r.set(i, "no sound")
				os.Remove(dir) // only if still empty
				return
			}
		}
		r.set(i, "retrying")
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		log.Printf("record: feed %d (%s): %v; retrying in %v", i+1, f.Name, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, time.Minute)
	}
}

type limited struct {
	b *strings.Builder
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.b.Len() < l.n {
		l.b.Write(p[:min(len(p), l.n-l.b.Len())])
	}
	return len(p), nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ---------- Listing and pruning ----------

var fileName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})_(\d{2})-(\d{2})-(\d{2})\.m4a$`)

// File is one recorded segment.
type File struct {
	Name  string    `json:"name"`
	Start time.Time `json:"start"`
	Size  int64     `json:"size"`
}

// Folder is one feed's recordings, newest first.
type Folder struct {
	Name  string `json:"name"`
	Files []File `json:"files"`
}

// List returns every feed folder under dir with its recordings, newest first.
func List(dir string) ([]Folder, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Folder
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fd := Folder{Name: e.Name()}
		files, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		for _, f := range files {
			if t, ok := startOf(f.Name()); ok {
				var size int64
				if info, err := f.Info(); err == nil {
					size = info.Size()
				}
				fd.Files = append(fd.Files, File{Name: f.Name(), Start: t, Size: size})
			}
		}
		sort.Slice(fd.Files, func(i, j int) bool { return fd.Files[i].Start.After(fd.Files[j].Start) })
		out = append(out, fd)
	}
	return out, nil
}

func startOf(name string) (time.Time, bool) {
	m := fileName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02 15 04 05", m[1]+" "+m[2]+" "+m[3]+" "+m[4], time.Local)
	return t, err == nil
}

// Path returns the file for a folder and file name from a request, or false
// if they do not name a recording inside dir.
func Path(dir, folder, file string) (string, bool) {
	if folder == "" || strings.ContainsAny(folder, `/\`) || folder == "." || folder == ".." || !fileName.MatchString(file) {
		return "", false
	}
	p := filepath.Join(dir, folder, file)
	if rel, err := filepath.Rel(dir, p); err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return p, true
}

// Prune deletes recordings that started more than Keep before now.
func (r *Recorder) Prune(now time.Time) {
	if r.Keep <= 0 {
		return
	}
	filepath.WalkDir(r.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if t, ok := startOf(d.Name()); ok && now.Sub(t) > r.Keep+r.Segment {
			if err := os.Remove(p); err == nil {
				log.Printf("record: removed %s (older than %v)", p, r.Keep)
			}
		}
		return nil
	})
}
