// Package compose turns a solved wall into one FFmpeg command that tiles every
// feed into a single video: a file to record, a stream for VLC, or a restream.
package compose

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vinodhalaharvi/camwall/feeds"
	"github.com/vinodhalaharvi/camwall/layout"
)

// Rect is a rectangle in pixels.
type Rect struct{ X, Y, W, H int }

// Options control the combined picture and its encoding.
type Options struct {
	Width, Height int    // output size, both even
	FPS           int    // output frame rate
	Gutter        int    // pixels between feeds
	Background    string // FFmpeg colour, e.g. black or 0x101010
	Labels        bool   // draw each feed's name (needs FFmpeg with drawtext)
	Font          string // font file for labels; empty lets FFmpeg choose
	Output        string // file, URL, or "-" for MPEG-TS on stdout
	Format        string // force the output container (-f); empty guesses
	Encoder       string // video encoder; default libx264 (also libx265, h264_videotoolbox, h264_nvenc …)
	CRF           int    // quality for libx264/libx265; lower is better
	Bitrate       string // target bitrate for other encoders, e.g. 6M
	Audio         int    // feed number (from 1) whose sound goes into the stream; 0 for none
	// Start is the Unix time in seconds at which FFmpeg is launched. Inputs
	// are stamped with wall-clock time, and the wall's timeline starts here.
	Start float64
}

func even(x float64) int { return 2 * int(math.Round(x/2)) }

// Place converts a wall's tiles to pixel rectangles on the output. Cell edges
// are rounded once, so neighbouring cells share an edge exactly; each feed is
// then fitted inside its cell less half the gutter on every side, keeping its
// shape, and centred. Every size and position is even (needed by yuv420p).
func Place(w layout.Wall, fs []feeds.Feed, o Options) []Rect {
	out := make([]Rect, len(w.Tiles))
	half := float64(o.Gutter) / 2
	for _, t := range w.Tiles {
		x0, x1 := float64(t.X)*float64(o.Width)+half, float64(t.X+t.W)*float64(o.Width)-half
		y0, y1 := float64(t.Y)*float64(o.Height)+half, float64(t.Y+t.H)*float64(o.Height)-half
		cw, ch := math.Max(x1-x0, 2), math.Max(y1-y0, 2)
		a := fs[t.Feed].Aspect
		fw, fh := cw, cw/a
		if fh > ch {
			fw, fh = ch*a, ch
		}
		r := Rect{W: max(2, 2*int(fw/2)), H: max(2, 2*int(fh/2))} // floor to even: never spill out of the cell
		r.X = even(x0 + (cw-float64(r.W))/2)
		r.Y = even(y0 + (ch-float64(r.H))/2)
		r.X = min(max(r.X, 0), o.Width-r.W)
		r.Y = min(max(r.Y, 0), o.Height-r.H)
		out[t.Feed] = r
	}
	return out
}

// escape quotes text for a filter argument.
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `'\''`, `:`, `\:`, `%`, `\%`)
	return "'" + r.Replace(s) + "'"
}

// Filter builds the filter graph. Live inputs carry wall-clock timestamps
// (see feeds.WallclockInputArgs) and are shifted so the wall starts at
// o.Start; paced inputs start from zero when the graph starts and are shifted
// by the graph's own start time. Each is then resampled to the output rate
// (keeping the newest frame in each slot), scaled into its rectangle, and laid
// over a background of the output size.
func Filter(rects []Rect, fs []feeds.Feed, o Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "color=c=%s:s=%dx%d:r=%d[bg0];", o.Background, o.Width, o.Height, o.FPS)
	for i, r := range rects {
		fmt.Fprintf(&b, "[%d:v]setpts=%s,fps=%d,", i, shift(fs[i], o), o.FPS)
		if fs[i].Filter != "" {
			b.WriteString(fs[i].Filter + ",")
		}
		fmt.Fprintf(&b, "scale=%d:%d:flags=bilinear,setsar=1,format=yuv420p", r.W, r.H)
		if o.Labels {
			size := max(12, min(32, r.H/14))
			font := ""
			if o.Font != "" {
				font = "fontfile=" + escape(o.Font) + ":"
			}
			fmt.Fprintf(&b, ",drawtext=%stext=%s:fontsize=%d:fontcolor=white:box=1:boxcolor=black@0.55:boxborderw=%d:x=%d:y=h-th-%d",
				font, escape(fs[i].Name), size, size/3, size/2, size/2)
		}
		fmt.Fprintf(&b, "[v%d];", i)
	}
	if a := o.Audio - 1; a >= 0 && a < len(fs) {
		fmt.Fprintf(&b, "[%d:a]asetpts=%s,aresample=async=1000,aformat=sample_rates=48000:channel_layouts=stereo[aout];", a, shift(fs[a], o))
	}
	for i, r := range rects {
		last := i == len(rects)-1
		fmt.Fprintf(&b, "[bg%d][v%d]overlay=x=%d:y=%d:eof_action=repeat:repeatlast=1", i, i, r.X, r.Y)
		if last {
			b.WriteString(",format=yuv420p[out]")
		} else {
			fmt.Fprintf(&b, "[bg%d];", i+1)
		}
	}
	return b.String()
}

// shift moves a feed's timestamps onto the wall's timeline (see Filter).
func shift(f feeds.Feed, o Options) string {
	if f.Kind == feeds.Live {
		return fmt.Sprintf("PTS-%.6f/TB", o.Start)
	}
	return fmt.Sprintf("PTS-STARTPTS+(RTCSTART/1000000-%.6f)/TB", o.Start)
}

// PlatformBitrate is YouTube's recommended video bitrate for a 30 fps live
// stream of the given height.
func PlatformBitrate(height int) string {
	switch {
	case height <= 720:
		return "4M"
	case height <= 1080:
		return "6M"
	case height <= 1440:
		return "10M"
	}
	return "20M"
}

// OutputFormat picks the container for a destination.
func OutputFormat(out string) string {
	if out == "-" {
		return "mpegts"
	}
	switch feeds.Scheme(out) {
	case "rtsp", "rtsps":
		return "rtsp"
	case "rtmp", "rtmps":
		return "flv"
	case "udp", "srt", "tcp", "rtp":
		return "mpegts"
	case "":
		switch strings.ToLower(filepath.Ext(out)) {
		case ".m3u8":
			return "hls"
		case ".ts":
			return "mpegts"
		}
	}
	return "" // let FFmpeg go by the file extension
}

// Args is the full FFmpeg argument list (without the program name).
func Args(w layout.Wall, fs []feeds.Feed, o Options) []string {
	rects := Place(w, fs, o)
	// -copyts: keep the wall-clock stamps of live inputs as they are, instead
	// of shifting each input to start at zero (which would undo the shared clock).
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-copyts"}
	for _, f := range fs {
		args = append(args, f.WallclockInputArgs(o.FPS)...)
	}
	enc := o.Encoder
	if enc == "" {
		enc = "libx264"
	}
	format := o.Format
	if format == "" {
		format = OutputFormat(o.Output)
	}
	// Live platforms (YouTube, Twitch …, reached over RTMP) refuse a stream
	// without sound and want a constant bitrate.
	platform := format == "flv"
	hasAudio := o.Audio-1 >= 0 && o.Audio-1 < len(fs)
	if platform && !hasAudio {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=44100")
	}
	args = append(args, "-filter_complex", Filter(rects, fs, o), "-map", "[out]")
	switch {
	case hasAudio:
		args = append(args, "-map", "[aout]", "-c:a", "aac", "-b:a", "128k")
	case platform:
		args = append(args, "-map", strconv.Itoa(len(fs))+":a", "-c:a", "aac", "-b:a", "128k")
	default:
		args = append(args, "-an")
	}
	args = append(args, "-c:v", enc)
	br := o.Bitrate
	if br == "" {
		br = PlatformBitrate(o.Height)
	}
	switch {
	case platform && (enc == "libx264" || enc == "libx265"):
		args = append(args, "-preset", "veryfast", "-tune", "zerolatency", "-b:v", br, "-minrate", br, "-maxrate", br, "-bufsize", br)
		if enc == "libx264" {
			args = append(args, "-x264-params", "nal-hrd=cbr:force-cfr=1")
		}
	case enc == "libx264":
		args = append(args, "-preset", "veryfast", "-tune", "zerolatency", "-crf", strconv.Itoa(o.CRF))
	case enc == "libx265":
		args = append(args, "-preset", "veryfast", "-tune", "zerolatency", "-crf", strconv.Itoa(o.CRF+5), "-tag:v", "hvc1")
	default:
		args = append(args, "-b:v", br)
		if platform {
			args = append(args, "-maxrate", br, "-bufsize", br)
		}
	}
	if !strings.Contains(enc, "videotoolbox") && !strings.Contains(enc, "vaapi") && !strings.Contains(enc, "qsv") {
		args = append(args, "-pix_fmt", "yuv420p")
	}
	args = append(args, "-g", strconv.Itoa(2*o.FPS), "-r", strconv.Itoa(o.FPS))
	switch format {
	case "rtsp":
		args = append(args, "-rtsp_transport", "tcp")
	case "hls":
		args = append(args, "-hls_time", "2", "-hls_list_size", "6", "-hls_flags", "delete_segments")
	}
	if format != "" {
		args = append(args, "-f", format)
	}
	if feeds.Scheme(o.Output) == "" && o.Output != "-" {
		args = append(args, "-y")
	}
	if o.Output == "-" {
		return append(args, "pipe:1")
	}
	return append(args, o.Output)
}

// Quote renders an argument list as a shell command a person can paste.
// Sources may hold passwords, so the result is for the person running it.
func Quote(prog string, args []string) string {
	parts := []string{prog}
	for _, a := range args {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=+,@%", r))
		}) < 0 {
			parts = append(parts, a)
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}
