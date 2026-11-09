// Package feeds describes the sources on a wall (cameras, streams, web
// videos, capture devices, video files, animations and still images) and how
// FFmpeg should open each one.
package feeds

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Kind says how a source behaves over time.
type Kind string

const (
	Live  Kind = "live"  // a camera, stream or capture device: never ends, not paced by us
	Video Kind = "video" // a file or recorded web video: played in real time and looped
	GIF   Kind = "gif"   // an animation (GIF, animated PNG or WebP): looped
	Image Kind = "image" // a still picture: held
)

// Feed is one source on the wall.
type Feed struct {
	Src    string  `json:"-"` // as given; may hold credentials, so never sent to browsers
	Name   string  `json:"name"`
	Kind   Kind    `json:"kind"`
	Aspect float64 `json:"aspect"`
	Weight float64 `json:"weight"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	Note   string  `json:"note,omitempty"`

	// How FFmpeg opens it.
	Input     string // what follows -i: Src, a device, a converted file or a resolved URL
	Format    string // forced input format (-f): v4l2, avfoundation, dshow, apng, lavfi …
	Size      string // capture size for devices (-video_size)
	FPS       int    // capture rate for devices (-framerate)
	Filter    string // extra FFmpeg video filter applied before scaling (crop, v360 …)
	Transport string // RTSP transport: auto (TCP, then UDP), tcp or udp
	Web       bool   // a web page (YouTube, Twitch …): resolved with yt-dlp each time it opens
	Audio     bool   // has an audio stream (found by probing)
	Browser   string // local file the browser can show itself (picture or animation)

	fixed bool // aspect given by the user; do not probe
}

// Pictures FFmpeg can usually read. Browsers show the native ones as they are;
// the rest are converted to JPEG once at start-up.
var (
	imageExt   = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".avif": true, ".bmp": true, ".tif": true, ".tiff": true, ".heic": true, ".heif": true, ".jxl": true, ".tga": true}
	browserExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".avif": true, ".gif": true}
	videoExt   = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".ts": true, ".mts": true, ".m2ts": true, ".flv": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".ogv": true, ".3gp": true, ".dav": true, ".h264": true, ".h265": true, ".264": true, ".265": true, ".hevc": true}
)

// Capture-device input formats, written as FORMAT:TARGET.
var deviceFormats = map[string]bool{"v4l2": true, "video4linux2": true, "avfoundation": true, "dshow": true,
	"gdigrab": true, "x11grab": true, "kmsgrab": true, "decklink": true, "lavfi": true, "android_camera": true}

// Sites whose pages hold video that yt-dlp can find.
var webHosts = []string{"youtube.com", "youtu.be", "twitch.tv", "vimeo.com", "dailymotion.com", "facebook.com",
	"fb.watch", "kick.com", "tiktok.com", "instagram.com", "x.com", "twitter.com", "bilibili.com", "rumble.com", "streamable.com", "afreecatv.com", "earthcam.com"}

func isWebPage(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	for _, w := range webHosts {
		if h == w || strings.HasSuffix(h, "."+w) {
			return true
		}
	}
	return false
}

// KindOf guesses a source's kind from its URL scheme or file extension.
func KindOf(src string) Kind {
	if u, err := url.Parse(src); err == nil && len(u.Scheme) > 1 {
		switch s := strings.ToLower(u.Scheme); {
		case s == "http" || s == "https":
			ext := strings.ToLower(filepath.Ext(u.Path))
			switch {
			case isWebPage(u):
				return Live // decided when yt-dlp resolves it
			case ext == ".gif":
				return GIF
			case imageExt[ext]:
				return Image
			case videoExt[ext]:
				return Video
			}
			return Live // HLS playlists, MJPEG cameras, other streams
		case s == "file":
			return KindOf(u.Path)
		default:
			return Live // rtsp, rtmp, srt, udp, devices …
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
	if strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") {
		return ""
	}
	if len(src) > 2 && src[1] == ':' && (src[2] == '\\' || src[2] == '/') {
		return "" // C:\video.mp4
	}
	if u, err := url.Parse(src); err == nil && len(u.Scheme) > 1 {
		return strings.ToLower(u.Scheme)
	}
	if i := strings.IndexByte(src, ':'); i > 1 && deviceFormats[strings.ToLower(src[:i])] {
		return strings.ToLower(src[:i]) // avfoundation:0 is not a valid URL
	}
	return ""
}

// IsFile reports whether a source is a path on this computer.
func (f Feed) IsFile() bool {
	return Scheme(f.Src) == "" && f.Format == "" && f.Src != "webcam" && f.Src != "screen" &&
		!strings.HasPrefix(f.Src, "webcam:") && !strings.HasPrefix(f.Src, "screen:")
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

// New makes a feed from a source with default name, weight and input.
// Besides files and URLs it understands:
//
//	webcam, webcam:N        the computer's Nth camera (from 0)
//	screen, screen:N        the computer's Nth screen
//	FORMAT:TARGET           any FFmpeg capture device: v4l2:/dev/video2,
//	                        avfoundation:1, dshow:video=Logitech C920,
//	                        x11grab::0.0, decklink:DeckLink Mini Recorder,
//	                        lavfi:testsrc2
//	ytdl:URL                a web page for yt-dlp (YouTube and Twitch are
//	                        recognised without it)
func New(src string) Feed {
	f := Feed{Src: src, Input: src, Name: DefaultName(src), Kind: KindOf(src), Weight: 1, Transport: "auto"}
	low := strings.ToLower(src)
	switch {
	case low == "webcam" || strings.HasPrefix(low, "webcam:"):
		n := strings.TrimPrefix(strings.TrimPrefix(low, "webcam"), ":")
		if n == "" {
			n = "0"
		}
		f.Kind, f.Name = Live, "Camera "+n
		switch runtime.GOOS {
		case "darwin":
			f.Format, f.Input, f.FPS = "avfoundation", n+":none", 30
		case "windows":
			f.Format, f.Input = "dshow", "" // needs the device's name: see camwall devices
			f.Note = "on Windows, name the camera: dshow:video=NAME (camwall devices lists them)"
		default:
			f.Format, f.Input = "v4l2", "/dev/video"+n
		}
	case low == "screen" || strings.HasPrefix(low, "screen:"):
		n := strings.TrimPrefix(strings.TrimPrefix(low, "screen"), ":")
		if n == "" {
			n = "0"
		}
		f.Kind, f.Name, f.FPS = Live, "Screen "+n, 15
		switch runtime.GOOS {
		case "darwin":
			f.Format, f.Input = "avfoundation", "Capture screen "+n+":none"
		case "windows":
			f.Format, f.Input = "gdigrab", "desktop"
		default:
			display := os.Getenv("DISPLAY")
			if display == "" {
				display = ":0"
			}
			if i := strings.LastIndexByte(display, ':'); i < 0 || !strings.Contains(display[i:], ".") {
				display += ".0" // a screen number, unless DISPLAY names one (":0.0")
			}
			f.Format, f.Input = "x11grab", display
		}
	case strings.HasPrefix(low, "ytdl:"):
		f.Src = src[len("ytdl:"):]
		f.Input, f.Name, f.Kind, f.Web = f.Src, DefaultName(f.Src), Live, true
	default:
		if i := strings.IndexByte(src, ':'); i > 1 && deviceFormats[strings.ToLower(src[:i])] {
			f.Format, f.Input, f.Kind = strings.ToLower(src[:i]), src[i+1:], Live
			if f.Format == "video4linux2" {
				f.Format = "v4l2"
			}
			f.Name = strings.TrimPrefix(f.Input, "video=")
			if f.Format == "lavfi" {
				f.Name, f.Kind = strings.SplitN(f.Input, "=", 2)[0], Video // generated: pace it like a file
			}
			break
		}
		if u, err := url.Parse(src); err == nil && (u.Scheme == "http" || u.Scheme == "https") && isWebPage(u) {
			f.Web = true
		}
	}
	return f
}

// option names that may follow name= on a line
var optionKeys = []string{"weight", "aspect", "format", "size", "fps", "filter", "transport"}

func isOption(tok string) bool {
	for _, k := range optionKeys {
		if strings.HasPrefix(tok, k+"=") {
			return true
		}
	}
	return false
}

// UniqueNames numbers feeds that share a name ("Door", "Door 2"), so every
// tile, saved setting and recordings folder can tell them apart.
func UniqueNames(fs []Feed) {
	seen := map[string]int{}
	for i := range fs {
		seen[strings.ToLower(fs[i].Name)]++
	}
	used := map[string]bool{}
	for _, f := range fs {
		if seen[strings.ToLower(f.Name)] < 2 {
			used[strings.ToLower(f.Name)] = true
		}
	}
	for i := range fs {
		base := fs[i].Name
		if seen[strings.ToLower(base)] < 2 {
			continue
		}
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s %d", base, n)
		}
		used[strings.ToLower(name)] = true
		fs[i].Name = name
	}
}

// ParseLine reads one line of a feeds file:
//
//	SOURCE [weight=N] [aspect=W:H] [format=F] [size=WxH] [fps=N]
//	       [filter=FFMPEG_FILTER] [transport=auto|tcp|udp] [name=NAME …]
//
// A SOURCE containing spaces (a device name) may be written in double quotes.
// Options may come in any order. name= takes the rest of the line, so names
// may contain spaces, except for other options at its end. Blank lines and
// lines starting with # are skipped (ok is false).
func ParseLine(line string) (f Feed, ok bool, err error) {
	line = strings.TrimSpace(strings.ReplaceAll(line, "\t", " ")) // tabs separate like spaces
	if line == "" || strings.HasPrefix(line, "#") {
		return f, false, nil
	}
	var src, rest string
	if strings.HasPrefix(line, `"`) {
		end := strings.Index(line[1:], `"`)
		if end < 0 {
			return f, false, fmt.Errorf("unclosed quote")
		}
		src, rest = line[1:1+end], line[2+end:]
	} else {
		src, rest, _ = strings.Cut(line, " ")
	}
	f = New(src)
	rest = strings.TrimSpace(rest)
	for rest != "" {
		var tok string
		if strings.HasPrefix(rest, "name=") {
			words := strings.Fields(strings.TrimPrefix(rest, "name="))
			for len(words) > 1 && isOption(words[len(words)-1]) {
				if err := f.option(words[len(words)-1]); err != nil {
					return f, false, err
				}
				words = words[:len(words)-1]
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
	case "format":
		if v == "" || strings.ContainsAny(v, " ,;") {
			return fmt.Errorf("format=%q: want an FFmpeg input format such as v4l2", v)
		}
		f.Format = v
		if f.Kind != Video && f.Kind != Image && f.Kind != GIF {
			f.Kind = Live
		}
	case "size":
		if _, err := ParseRatio(v); err != nil || !strings.Contains(v, "x") {
			return fmt.Errorf("size=%s: want WIDTHxHEIGHT", v)
		}
		f.Size = v
	case "fps":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 240 {
			return fmt.Errorf("fps=%s: want 1 to 240", v)
		}
		f.FPS = n
	case "filter":
		if v == "" {
			return fmt.Errorf("filter= is empty")
		}
		f.Filter = v
	case "transport":
		switch v {
		case "auto", "tcp", "udp":
			f.Transport = v
		default:
			return fmt.Errorf("transport=%s: want auto, tcp or udp", v)
		}
	default:
		return fmt.Errorf("unknown option %q (want %s= or name=)", k, strings.Join(optionKeys, "=, "))
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

// ---------- FFmpeg input options ----------

// InputArgs are the FFmpeg options that open the feed as an input, ending
// with -i INPUT. fps paces still images.
func (f Feed) InputArgs(fps int) []string {
	return f.inputArgs(fps, false)
}

// WallclockInputArgs are InputArgs for a combined wall. A live feed's
// packets are stamped with the time they are read, so all live inputs share
// one clock: a camera that buffered video while FFmpeg opened the others is
// read as a burst with nearly equal stamps, and the frame-rate step keeps only
// its newest frame, so every tile shows "now" however long start-up took.
// Files, animations and images are paced by -re instead (which cannot work
// with wall-clock stamps) and keep their own timestamps.
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
		a = append(a, f.netArgs()...) // a picture on a server
	case GIF:
		a = append(a, "-re", "-ignore_loop", "0")
		a = append(a, f.netArgs()...)
	case Video:
		a = append(a, "-re")
		if f.Format != "lavfi" {
			a = append(a, "-stream_loop", "-1")
		}
		a = append(a, f.netArgs()...) // a recorded web video or a file on a server
	case Live:
		// FFmpeg opens inputs one at a time; probing each for half a second
		// instead of five keeps start-up short on a wall of cameras.
		a = append(a, "-analyzeduration", "500000", "-probesize", "1000000")
		a = append(a, f.netArgs()...)
		a = append(a, "-fflags", "+discardcorrupt")
	}
	a = append(a, f.deviceArgs()...)
	return append(a, "-i", f.Input)
}

// netArgs are timeouts and transport options for network sources.
func (f Feed) netArgs() []string {
	switch Scheme(f.Input) {
	case "rtsp", "rtsps":
		a := []string{"-timeout", "10000000"}
		switch f.Transport {
		case "tcp", "udp":
			return append(a, "-rtsp_transport", f.Transport)
		}
		return append(a, "-rtsp_flags", "prefer_tcp") // TCP if the camera offers it, else UDP
	case "http", "https":
		return []string{"-rw_timeout", "10000000", "-reconnect", "1", "-reconnect_streamed", "1"}
	case "udp", "srt":
		// give up on a silent source instead of waiting for ever (then retry)
		return []string{"-timeout", "10000000"}
	case "rtmp", "rtmps", "tcp":
		return []string{"-rw_timeout", "10000000"}
	}
	return nil
}

// deviceArgs force the input format and capture settings.
func (f Feed) deviceArgs() []string {
	var a []string
	if f.Format != "" {
		a = append(a, "-f", f.Format)
	}
	if f.Size != "" {
		a = append(a, "-video_size", f.Size)
	}
	if f.FPS > 0 && f.Kind == Live {
		a = append(a, "-framerate", strconv.Itoa(f.FPS))
	}
	return a
}
