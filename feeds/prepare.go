package feeds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tools are the programs feeds are opened with.
type Tools struct {
	FFmpeg  string
	FFprobe string // empty: shapes are not probed (16:9 is assumed)
	YtDlp   string // empty: web pages cannot be opened
	TempDir string // where converted pictures go
}

// ---------- Web pages ----------

// ErrNoYtDlp is returned for a web page when yt-dlp is not installed.
var ErrNoYtDlp = errors.New("this is a web page; install yt-dlp to open it (brew install yt-dlp, or pip install yt-dlp)")

// Resolve asks yt-dlp for the media URL behind a web page, at most 720p and
// with video, and whether it is live. The URL expires, so it is resolved each
// time FFmpeg opens the feed.
func Resolve(ctx context.Context, ytdlp, page string) (media string, live bool, err error) {
	if ytdlp == "" {
		return "", false, ErrNoYtDlp
	}
	if strings.HasPrefix(page, "-") {
		return "", false, fmt.Errorf("not a web page: %q", page)
	}
	// "--": the page is never read as one of yt-dlp's options
	out, err := Child(exec.CommandContext(ctx, ytdlp, "--no-warnings", "--no-playlist",
		"-f", "best[height<=720][vcodec!=none]/bestvideo[height<=720]/best",
		"--print", "is_live", "--print", "url", "--", page)).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", false, fmt.Errorf("yt-dlp: %s", firstLine(string(ee.Stderr)))
		}
		return "", false, err
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) < 2 || !(strings.HasPrefix(lines[len(lines)-1], "https://") || strings.HasPrefix(lines[len(lines)-1], "http://")) {
		return "", false, fmt.Errorf("yt-dlp found no video on %s", page)
	}
	return lines[len(lines)-1], lines[0] == "True", nil
}

// Opened returns the feed ready for FFmpeg: a web page is resolved to its
// current media URL; anything else is returned as it is.
func (f Feed) Opened(ctx context.Context, t Tools) (Feed, error) {
	if !f.Web {
		return f, nil
	}
	media, live, err := Resolve(ctx, t.YtDlp, f.Src)
	if err != nil {
		return f, err
	}
	f.Input = media
	if live {
		f.Kind = Live
	} else {
		f.Kind = Video
	}
	return f, nil
}

// ---------- Pictures and animations ----------

// animated reports whether a PNG (acTL chunk) or WebP (ANIM chunk) file is an
// animation, from its first 64 KB.
func animated(path string) (format string, ok bool) {
	fh, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer fh.Close()
	b := make([]byte, 64<<10)
	n, _ := fh.Read(b)
	b = b[:n]
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG")):
		i, j := bytes.Index(b, []byte("acTL")), bytes.Index(b, []byte("IDAT"))
		return "apng", i > 0 && (j < 0 || i < j)
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp", bytes.Contains(b[:min(len(b), 4096)], []byte("ANIM"))
	}
	return "", false
}

// webpSize reads the canvas size of an extended (VP8X) WebP file, the kind
// animations use.
func webpSize(path string) (int, int) {
	b := make([]byte, 30)
	fh, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer fh.Close()
	if n, _ := fh.Read(b); n < 30 || string(b[0:4]) != "RIFF" || string(b[8:16]) != "WEBPVP8X" {
		return 0, 0
	}
	w := 1 + int(b[24]) | int(b[25])<<8 | int(b[26])<<16
	h := 1 + int(b[27]) | int(b[28])<<8 | int(b[29])<<16
	return w, h
}

// decodes reports whether FFmpeg can read a frame of the feed.
func decodes(ctx context.Context, ffmpeg string, f Feed) bool {
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, f.deviceArgs()...)
	args = append(args, "-i", f.Input, "-frames:v", "1", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	return cmd.Run() == nil && stderr.Len() == 0
}

// toJPEG converts a picture to JPEG with FFmpeg, or with sips on macOS (which
// reads HEIC, and WebP that older FFmpeg cannot).
func toJPEG(ctx context.Context, ffmpeg, src, dst string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", src,
		"-frames:v", "1", "-q:v", "3", dst)
	ffErr := cmd.Run()
	if ffErr == nil {
		if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
			return nil
		}
	}
	if runtime.GOOS == "darwin" {
		if sips, err := exec.LookPath("sips"); err == nil {
			if exec.CommandContext(ctx, sips, "-s", "format", "jpeg", src, "--out", dst).Run() == nil {
				return nil
			}
		}
	}
	if ffErr == nil {
		ffErr = errors.New("no picture written")
	}
	return fmt.Errorf("cannot read this picture (%v)", ffErr)
}

// preparePicture settles how a local picture or animation is shown: browsers
// get the file itself when they can show it, or a JPEG copy; FFmpeg gets the
// file, or a JPEG copy when it cannot decode the original. Animated PNG and
// WebP become animations.
func preparePicture(ctx context.Context, t Tools, f *Feed, n int) error {
	ext := strings.ToLower(filepath.Ext(f.Src))
	if format, anim := animated(f.Src); anim {
		f.Kind = GIF
		if format == "apng" {
			f.Format = "apng"
		}
	}
	if browserExt[ext] {
		f.Browser = f.Src
	}
	if decodes(ctx, t.FFmpeg, *f) {
		if f.Browser == "" { // TIFF, BMP, HEIC …: give browsers a JPEG
			jpg := filepath.Join(t.TempDir, fmt.Sprintf("feed%d.jpg", n+1))
			if err := toJPEG(ctx, t.FFmpeg, f.Src, jpg); err != nil {
				return err
			}
			f.Browser = jpg
		}
		return nil
	}
	// FFmpeg cannot decode it (HEIC without libheif, animated WebP before
	// FFmpeg 8): show its first frame as a still.
	jpg := filepath.Join(t.TempDir, fmt.Sprintf("feed%d.jpg", n+1))
	if err := toJPEG(ctx, t.FFmpeg, f.Src, jpg); err != nil {
		w, h := webpSize(f.Src)
		if f.Browser == "" || w == 0 {
			return err
		}
		// browsers can still show it; the combined stream gets a blank tile
		f.Note = "your FFmpeg cannot read this file (FFmpeg 8 can); it shows in the browser wall but is blank in the combined stream"
		f.Kind, f.Format, f.Input = Video, "lavfi", fmt.Sprintf("color=c=black:s=%dx%d:r=5", w&^1, h&^1)
		f.Width, f.Height, f.Aspect, f.fixed = w, h, float64(w)/float64(h), true
		return nil
	}
	if f.Kind == GIF {
		f.Note = "your FFmpeg cannot play this animation; the combined stream shows its first frame"
	} else {
		f.Browser = jpg
	}
	f.Kind, f.Input, f.Format = Image, jpg, ""
	return nil
}

// ---------- Probing ----------

type probeOut struct {
	Streams []struct {
		Type     string `json:"codec_type"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		SAR      string `json:"sample_aspect_ratio"`
		SideData []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
		Tags map[string]string `json:"tags"`
	} `json:"streams"`
}

// Probe asks ffprobe for the feed's picture size and whether it has sound.
// It fills Aspect, Width, Height and Audio, honouring pixel shape and
// rotation. The feed must already be Opened.
func Probe(ctx context.Context, ffprobe string, f *Feed) error {
	args := []string{"-v", "error",
		"-show_entries", "stream=codec_type,width,height,sample_aspect_ratio:stream_side_data=rotation:stream_tags=rotate", "-of", "json"}
	args = append(args, f.netArgs()...)
	args = append(args, f.deviceArgs()...)
	args = append(args, "-i", f.Input)
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
	found := false
	for _, s := range p.Streams {
		if s.Type == "audio" {
			f.Audio = true
		}
		if s.Type != "video" || found || s.Width == 0 || s.Height == 0 {
			continue
		}
		found = true
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
	}
	if !found {
		return fmt.Errorf("no video found")
	}
	return nil
}

var (
	showinfoSize = regexp.MustCompile(`\ss:(\d+)x(\d+)\s`)
	showinfoSAR  = regexp.MustCompile(`\ssar:(\d+)/(\d+)\s`)
)

// ProbeFiltered finds the shape of the feed's picture after its filter= (a
// crop or a fisheye dewarp changes the shape) by running one frame through it.
func ProbeFiltered(ctx context.Context, ffmpeg string, f *Feed) error {
	args := []string{"-hide_banner", "-nostdin"}
	args = append(args, f.netArgs()...)
	args = append(args, f.deviceArgs()...)
	args = append(args, "-i", f.Input, "-vf", f.Filter+",showinfo", "-frames:v", "1", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	m := showinfoSize.FindStringSubmatch(stderr.String())
	sar := showinfoSAR.FindStringSubmatch(stderr.String())
	if m == nil {
		if err == nil {
			err = errors.New("no frame came out of the filter")
		}
		return fmt.Errorf("filter=%s: %s", f.Filter, lastLine(stderr.String(), err))
	}
	w, _ := strconv.ParseFloat(m[1], 64)
	h, _ := strconv.ParseFloat(m[2], 64)
	if sar != nil {
		sx, _ := strconv.ParseFloat(sar[1], 64)
		sy, _ := strconv.ParseFloat(sar[2], 64)
		if sx > 0 && sy > 0 {
			w *= sx / sy
		}
	}
	f.Width, f.Height, f.Aspect = int(w), int(h), w/h
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func lastLine(s string, fallback error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback.Error()
	}
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// Prepare gets every feed ready, in parallel: web pages are resolved (to
// learn whether they are live), pictures and animations are checked and
// converted where needed, and each feed's shape is probed. A feed that cannot
// be probed keeps a 16:9 shape (the usual camera) and gets a note rather than
// an error, so a camera that is offline at start-up still gets its tile.
// Only a broken local picture is an error.
func Prepare(fs []Feed, t Tools, timeout time.Duration) error {
	var wg sync.WaitGroup
	errs := make([]error, len(fs))
	for i := range fs {
		wg.Add(1)
		go func(i int, f *Feed) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if f.Format == "dshow" && f.Input == "" {
				errs[i] = errors.New(f.Note)
				return
			}
			if f.IsFile() && (f.Kind == Image || f.Kind == GIF || imageExt[strings.ToLower(filepath.Ext(f.Src))]) {
				if err := preparePicture(ctx, t, f, i); err != nil {
					errs[i] = fmt.Errorf("%s: %w", Redact(f.Src), err)
					return
				}
			}
			g := *f
			if f.Web {
				var err error
				if g, err = f.Opened(ctx, t); err != nil {
					if err == ErrNoYtDlp {
						errs[i] = fmt.Errorf("%s: %w", Redact(f.Src), err)
						return
					}
					f.Aspect, f.Note = 16.0/9, "could not open the page ("+Redact(err.Error())+"); assuming 16:9"
					return
				}
				f.Kind = g.Kind
			}
			*f = settle(*f, g, ctx, t)
		}(i, &fs[i])
	}
	wg.Wait()
	return errors.Join(errs...)
}

// settle probes the shape (and sound) of an opened feed g into f. With a
// filter=, the shape is the filter's output.
func settle(f, g Feed, ctx context.Context, t Tools) Feed {
	if f.fixed { // shape given: still find out whether it has sound
		if t.FFprobe != "" && f.Kind != Image && f.Kind != GIF {
			if err := Probe(ctx, t.FFprobe, &g); err != nil {
				f.Note = "could not check it for sound (" + Redact(err.Error()) + ")"
			} else {
				f.Audio = g.Audio
			}
		}
		return f
	}
	var err error
	if t.FFprobe != "" {
		err = Probe(ctx, t.FFprobe, &g)
	}
	if f.Filter != "" {
		err = ProbeFiltered(ctx, t.FFmpeg, &g)
	} else if t.FFprobe == "" {
		f.Aspect, f.Note = 16.0/9, "ffprobe not found; assuming 16:9"
		return f
	}
	if err != nil {
		reason := Redact(err.Error())
		if ctx.Err() != nil {
			reason = "no answer in time"
		}
		f.Aspect, f.Note = 16.0/9, "could not read its size ("+reason+"); assuming 16:9"
		return f
	}
	f.Aspect, f.Width, f.Height, f.Audio = g.Aspect, g.Width, g.Height, g.Audio
	return f
}
