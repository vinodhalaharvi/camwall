// Package feeds describes the sources on a wall (cameras, streams, video
// files, GIFs and still images) and how FFmpeg should open each one.
package feeds

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind says how a source behaves over time.
type Kind string

const (
	Live  Kind = "live"  // a camera or network stream: never ends, not paced by us
	Video Kind = "video" // a file: played in real time and looped
	GIF   Kind = "gif"   // an animated GIF: looped
	Image Kind = "image" // a still picture: held
)

// Feed is one source on the wall.
type Feed struct {
	Src    string  `json:"-"` // URL or path; may hold credentials, so never sent to browsers
	Name   string  `json:"name"`
	Kind   Kind    `json:"kind"`
	Aspect float64 `json:"aspect"`
	Weight float64 `json:"weight"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	fixed  bool    // aspect given by the user; do not probe
	Note   string  `json:"note,omitempty"`
}

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".bmp": true, ".tif": true, ".tiff": true}

// KindOf guesses a source's kind from its URL scheme or file extension.
func KindOf(src string) Kind {
	if u, err := url.Parse(src); err == nil && len(u.Scheme) > 1 {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			p := strings.ToLower(u.Path)
			switch {
			case strings.HasSuffix(p, ".gif"):
				return GIF
			case imageExt[filepath.Ext(p)]:
				return Image
			case strings.HasSuffix(p, ".mp4"), strings.HasSuffix(p, ".mov"), strings.HasSuffix(p, ".mkv"), strings.HasSuffix(p, ".webm"):
				return Video
			}
			return Live // HLS playlists, MJPEG cameras, other streams
		case "file":
			return KindOf(u.Path)
		default:
			return Live // rtsp, rtsps, rtmp, srt, udp, tcp …
		}
	}
	ext := strings.ToLower(filepath.Ext(src))
	switch {
	case ext == ".gif":
		return GIF
	case imageExt[ext]:
		return Image
	}
	return Video
}

// Scheme returns the lower-case URL scheme, or "" for a file path.
func Scheme(src string) string {
	if u, err := url.Parse(src); err == nil && len(u.Scheme) > 1 {
		return strings.ToLower(u.Scheme)
	}
	return ""
}

// DefaultName is a readable name for a source without credentials.
func DefaultName(src string) string {
	if u, err := url.Parse(src); err == nil && len(u.Scheme) > 1 && u.Host != "" {
		name := u.Hostname()
		if p := strings.Trim(u.Path, "/"); p != "" {
			name += "/" + p
		}
		return name
	}
	base := filepath.Base(src)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// New makes a feed from a source with default name and weight.
func New(src string) Feed {
	return Feed{Src: src, Name: DefaultName(src), Kind: KindOf(src), Weight: 1}
}

// ParseLine reads one line of a feeds file:
//
//	SOURCE [weight=N] [aspect=W:H] [name=NAME …]
//
// Options may come in any order. name= takes the rest of the line, so names
// may contain spaces, except for weight= and aspect= options at its end.
// Blank lines and lines starting with # are skipped (ok is false).
func ParseLine(line string) (f Feed, ok bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return f, false, nil
	}
	src, rest, _ := strings.Cut(line, " ")
	f = New(src)
	rest = strings.TrimSpace(rest)
	for rest != "" {
		var tok string
		if strings.HasPrefix(rest, "name=") {
			words := strings.Fields(strings.TrimPrefix(rest, "name="))
			// options after the name: peel weight=/aspect= off the end
			for len(words) > 1 {
				w := words[len(words)-1]
				if !strings.HasPrefix(w, "weight=") && !strings.HasPrefix(w, "aspect=") {
					break
				}
				words = words[:len(words)-1]
				if err := f.option(w); err != nil {
					return f, false, err
				}
			}
			f.Name = strings.Join(words, " ")
			if f.Name == "" {
				return f, false, fmt.Errorf("name= is empty")
			}
			break
		}
		tok, rest, _ = strings.Cut(rest, " ")
		rest = strings.TrimSpace(rest)
		if err := f.option(tok); err != nil {
			return f, false, err
		}
	}
	return f, true, nil
}

func (f *Feed) option(tok string) error {
	k, v, found := strings.Cut(tok, "=")
	if !found {
		return fmt.Errorf("%q: expected key=value", tok)
	}
	switch k {
	case "weight":
		w, err := strconv.ParseFloat(v, 64)
		if err != nil || !(w > 0) || w > 100 {
			return fmt.Errorf("weight=%s: want a number above 0, up to 100", v)
		}
		f.Weight = w
	case "aspect":
		a, err := ParseRatio(v)
		if err != nil {
			return err
		}
		f.Aspect, f.fixed = a, true
	default:
		return fmt.Errorf("unknown option %q (want weight=, aspect= or name=)", k)
	}
	return nil
}

// ReadFile reads a feeds file, one source per line.
func ReadFile(path string) ([]Feed, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return Read(fh, path)
}

// Read reads feeds lines from r; name is used in error messages.
func Read(r io.Reader, name string) ([]Feed, error) {
	var out []Feed
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		f, ok, err := ParseLine(sc.Text())
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, n, err)
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, sc.Err()
}

// ParseRatio reads 16:9, 16/9, 1.78, or 1920x1080.
func ParseRatio(s string) (float64, error) {
	for _, sep := range []string{":", "/", "x"} {
		if a, b, ok := strings.Cut(s, sep); ok {
			x, e1 := strconv.ParseFloat(a, 64)
			y, e2 := strconv.ParseFloat(b, 64)
			if e1 != nil || e2 != nil || !(x > 0) || !(y > 0) {
				return 0, fmt.Errorf("bad ratio %q", s)
			}
			return x / y, nil
		}
	}
	x, err := strconv.ParseFloat(s, 64)
	if err != nil || !(x > 0) {
		return 0, fmt.Errorf("bad ratio %q", s)
	}
	return x, nil
}

// InputArgs are the FFmpeg options that open the feed as an input, ending
// with -i SOURCE. fps paces still images.
func (f Feed) InputArgs(fps int) []string {
	return f.inputArgs(fps, false)
}

// WallclockInputArgs are InputArgs for a combined wall. A live feed's
// packets are stamped with the time they are read, so all live inputs share
// one clock: a camera that buffered video while FFmpeg opened the others is
// read as a burst with nearly equal stamps, and the frame-rate step keeps only
// its newest frame, so every tile shows "now" however long start-up took.
// Files, GIFs and images are paced by -re instead (which cannot work with
// wall-clock stamps) and keep their own timestamps.
func (f Feed) WallclockInputArgs(fps int) []string {
	return f.inputArgs(fps, f.Kind == Live)
}

func (f Feed) inputArgs(fps int, wallclock bool) []string {
	var a []string
	if wallclock {
		a = append(a, "-use_wallclock_as_timestamps", "1")
	}
	a = append(a, "-thread_queue_size", "64")
	switch f.Kind {
	case Image:
		a = append(a, "-re", "-loop", "1", "-framerate", strconv.Itoa(fps))
	case GIF:
		a = append(a, "-re", "-ignore_loop", "0")
	case Video:
		a = append(a, "-re", "-stream_loop", "-1")
	case Live:
		// FFmpeg opens inputs one at a time; probing each for half a second
		// instead of five keeps start-up short on a wall of cameras.
		a = append(a, "-analyzeduration", "500000", "-probesize", "1000000")
		switch Scheme(f.Src) {
		case "rtsp", "rtsps":
			a = append(a, "-rtsp_transport", "tcp", "-timeout", "10000000")
		case "http", "https":
			a = append(a, "-rw_timeout", "10000000", "-reconnect", "1", "-reconnect_streamed", "1")
		}
		a = append(a, "-fflags", "+discardcorrupt")
	}
	return append(a, "-i", f.Src)
}

// ---------- Probing ----------

type probeOut struct {
	Streams []struct {
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		SAR      string `json:"sample_aspect_ratio"`
		SideData []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
		Tags map[string]string `json:"tags"`
	} `json:"streams"`
}

// Probe asks ffprobe for the feed's picture size. It fills Aspect, Width
// and Height, honouring pixel shape and rotation.
func Probe(ctx context.Context, ffprobe string, f *Feed) error {
	args := []string{"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,sample_aspect_ratio:stream_side_data=rotation:stream_tags=rotate", "-of", "json"}
	in := f.InputArgs(1)
	for i := 0; i < len(in)-2; i++ { // keep only options ffprobe understands
		switch in[i] {
		case "-rtsp_transport", "-timeout", "-rw_timeout":
			args = append(args, in[i], in[i+1])
			i++
		}
	}
	args = append(args, f.Src)
	out, err := exec.CommandContext(ctx, ffprobe, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return fmt.Errorf("%s", firstLine(string(ee.Stderr)))
		}
		return err
	}
	var p probeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return err
	}
	if len(p.Streams) == 0 || p.Streams[0].Width == 0 || p.Streams[0].Height == 0 {
		return fmt.Errorf("no video found")
	}
	s := p.Streams[0]
	w, h := float64(s.Width), float64(s.Height)
	if n, d, ok := strings.Cut(s.SAR, ":"); ok {
		x, _ := strconv.ParseFloat(n, 64)
		y, _ := strconv.ParseFloat(d, 64)
		if x > 0 && y > 0 {
			w *= x / y
		}
	}
	rot := 0.0
	for _, sd := range s.SideData {
		rot = sd.Rotation
	}
	if r, err := strconv.ParseFloat(s.Tags["rotate"], 64); err == nil {
		rot = r
	}
	if math.Mod(math.Abs(rot), 180) == 90 {
		w, h = h, w
	}
	f.Width, f.Height, f.Aspect = s.Width, s.Height, w/h
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// ProbeAll probes every feed in parallel. A feed that cannot be probed keeps
// a 16:9 shape (the usual camera) and gets a note; it is not an error, so a
// camera that is offline at start-up still gets its tile.
func ProbeAll(ffprobe string, fs []Feed, timeout time.Duration) {
	var wg sync.WaitGroup
	for i := range fs {
		if fs[i].fixed {
			continue
		}
		wg.Add(1)
		go func(f *Feed) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := Probe(ctx, ffprobe, f); err != nil {
				if ctx.Err() != nil {
					err = fmt.Errorf("no answer within %v", timeout)
				}
				f.Aspect, f.Note = 16.0/9, "could not read its size ("+err.Error()+"); assuming 16:9"
			}
		}(&fs[i])
	}
	wg.Wait()
}
