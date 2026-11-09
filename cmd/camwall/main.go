// Command camwall puts every camera feed on one gap-free wall: in a browser,
// or as a single combined video stream made by FFmpeg.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vinodhalaharvi/camwall/compose"
	"github.com/vinodhalaharvi/camwall/feeds"
	"github.com/vinodhalaharvi/camwall/layout"
	"github.com/vinodhalaharvi/camwall/onvif"
	"github.com/vinodhalaharvi/camwall/record"
	"github.com/vinodhalaharvi/camwall/wall"
)

var version = "dev" // set by release builds with -ldflags "-X main.version=…"

func init() {
	// `go install …@v0.1.1` records the module version; use it when no
	// release build set one
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = strings.TrimPrefix(bi.Main.Version, "v")
		}
	}
}

const usage = `camwall puts every camera feed on one gap-free wall.

Usage:
  camwall serve    [flags] SOURCE...   watch the wall in a browser
  camwall stream   [flags] SOURCE...   combine the feeds into one video with FFmpeg
  camwall plan     [flags] SOURCE...   print the wall for a screen shape
  camwall record   [flags] SOURCE...   record the feeds' sound (no wall)
  camwall discover [flags]             find ONVIF cameras and print their RTSP addresses
  camwall devices                      list this computer's cameras and screens
  camwall laws                         verify the layout algebra against brute force
  camwall version

A SOURCE can be:
  rtsp://user:pass@192.168.1.20/stream1   an IP camera (RTSP, RTMP, SRT, UDP, HTTP/MJPEG)
  https://example.com/live/index.m3u8     an HLS or other network stream
  https://www.youtube.com/watch?v=…        a web video or live stream (needs yt-dlp)
  lobby.mp4, promo.gif, logo.png          video files (looped), animations, pictures
  webcam, webcam:1, screen                this computer's cameras and screens
  v4l2:/dev/video2, "dshow:video=NAME"    any FFmpeg capture device, as FORMAT:TARGET
Pictures in TIFF, BMP or HEIC are converted for browsers; animated GIF, PNG and
WebP play. Anything else FFmpeg reads works too.

Instead of listing sources, pass -f FILE with one per line, and options:
  rtsp://user:pass@192.168.1.20/stream1  name=Front door  weight=2
  rtsp://192.168.1.21/live               transport=udp name=Register
  rtsp://192.168.1.22/fisheye            filter=v360=fisheye:e:ih_fov=180:iv_fov=180 name=Ceiling
  "dshow:video=Integrated Camera"        size=1280x720 fps=30 name=Desk
  floorplan.png                          aspect=4:3 name=Floor plan
weight=N asks for N times the area; aspect=W:H skips probing; transport= is
auto (TCP, else UDP), tcp or udp; filter= is any FFmpeg video filter (crop,
rotate, dewarp); format=, size= and fps= set capture options; name= takes the
rest of the line.

Flags for serve, stream and plan:
  -f FILE          read sources from FILE
  -ffmpeg PATH     FFmpeg to use (default: ffmpeg on your PATH)

serve flags:
  -addr ADDR       where to listen (default 127.0.0.1:7788; use :7788 to let
                   other devices on your network watch)
  -password PW     ask viewers for this password (user name: camwall)
  -title TEXT      name shown on the page (default camwall)
  -fps N           frames per second sent to browsers (default 10)
  -max PX          longest side of each feed in the browser (default 960)
  -no-open         don't open a browser
  -record DIR      also record the sound of every feed that has some into DIR;
                   the page then has a Recordings view (see record flags)

record flags (also for serve -record):
  -o DIR           where to record (camwall record; default recordings)
  -keep AGE        delete recordings older than this: 7d (default), 36h, 0 keeps all
  -segment D       length of each file (default 15m), aligned to the clock

stream flags:
  -o DEST          file (.mp4 .mkv .ts .m3u8), URL (udp:// srt:// rtsp:// rtmp://),
                   or - for MPEG-TS on stdout (default wall.mkv)
  -size WxH        output size (default 1920x1080)
  -fps N           frame rate (default 15)
  -gutter PX       space between feeds (default 4)
  -bg COLOR        background (default black)
  -labels          draw each feed's name (needs FFmpeg built with drawtext)
  -audio N         include the sound of feed N (numbered from 1)
  -encoder NAME    video encoder: libx264 (default), libx265, h264_videotoolbox,
                   h264_nvenc, h264_qsv …
  -crf N           x264/x265 quality, lower is better (default 23)
  -bitrate RATE    bitrate for other encoders and for RTMP (YouTube, Twitch),
                   which get a constant bitrate (default 4M at 720p, 6M at 1080p)
                   and a silent sound track unless -audio is given
  -dry-run         print the FFmpeg command instead of running it
  -once            don't restart FFmpeg when it stops

plan flags:
  -ratio R         screen shape: 16:9, 21:9, 9:16, 1.6 … (default 16:9)

discover flags:
  -user NAME, -password PW   the cameras' login (most cameras need one)
  -sub             print each camera's sub-stream (lighter) instead of its main stream
  -timeout D       how long to listen for answers (default 3s)

Examples:
  camwall discover -user admin -password secret > cams.txt
  camwall serve -f cams.txt -addr :7788 -password s3cret
  camwall stream -f cams.txt -audio 1 -o - | vlc -
  camwall stream -f cams.txt -o rtsp://localhost:8554/wall
  camwall stream -f cams.txt -o rtmp://a.rtmp.youtube.com/live2/YOUR-KEY
`

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help":
		fmt.Print(usage)
		return
	case args[0] == "version" || args[0] == "--version":
		fmt.Println("camwall", version)
		return
	case args[0] == "serve":
		err = serve(args[1:])
	case args[0] == "stream":
		err = stream(args[1:])
	case args[0] == "plan":
		err = plan(args[1:])
	case args[0] == "laws":
		err = laws()
	case args[0] == "record":
		err = recordCmd(args[1:])
	case args[0] == "devices":
		err = devices()
	case args[0] == "discover":
		err = discover(args[1:])
	default:
		err = fmt.Errorf("unknown command %q (try camwall help)", args[0])
	}
	if tmpDir != "" {
		os.RemoveAll(tmpDir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "camwall:", err)
		os.Exit(1)
	}
}

// common holds flags every command shares.
type common struct {
	file, ffmpeg string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.file, "f", "", "")
	fs.StringVar(&c.ffmpeg, "ffmpeg", "ffmpeg", "")
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs
}

// parse handles flags given before or after the sources.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

// tmpDir holds pictures converted for browsers; removed when camwall exits.
var tmpDir string

// load reads the sources, finds FFmpeg (and ffprobe and yt-dlp) and gets
// every feed ready: pictures converted where needed, web pages resolved,
// shapes probed.
func (c *common) load(srcs []string, needFFmpeg bool) ([]feeds.Feed, feeds.Tools, error) {
	var t feeds.Tools
	var fs []feeds.Feed
	if c.file != "" {
		list, err := feeds.ReadFile(c.file)
		if err != nil {
			return nil, t, err
		}
		fs = append(fs, list...)
	}
	for _, s := range srcs {
		fs = append(fs, feeds.New(s))
	}
	if len(fs) == 0 {
		return nil, t, errors.New("no sources: list camera URLs or files, or use -f FILE (see camwall help)")
	}
	if len(fs) > layout.MaxFeeds {
		return nil, t, fmt.Errorf("%d sources; one wall takes at most %d (run a second wall for the rest)", len(fs), layout.MaxFeeds)
	}
	for _, f := range fs {
		if f.IsFile() {
			if _, err := os.Stat(f.Src); err != nil {
				return nil, t, fmt.Errorf("%s: %w", f.Src, err)
			}
		}
	}
	ff, err := exec.LookPath(c.ffmpeg)
	if err != nil {
		if needFFmpeg {
			return nil, t, fmt.Errorf("FFmpeg not found (%s); install it, e.g. brew install ffmpeg", c.ffmpeg)
		}
		for i := range fs {
			if !(fs[i].Aspect > 0) {
				fs[i].Aspect = 16.0 / 9
			}
		}
		return fs, feeds.Tools{FFmpeg: c.ffmpeg}, nil
	}
	t.FFmpeg = ff
	probe := "ffprobe"
	if dir := strings.TrimSuffix(ff, "ffmpeg"); dir != ff {
		if _, err := os.Stat(dir + "ffprobe"); err == nil {
			probe = dir + "ffprobe"
		}
	}
	if p, err := exec.LookPath(probe); err == nil {
		t.FFprobe = p
	} else {
		fmt.Fprintln(os.Stderr, "camwall: ffprobe not found; assuming 16:9 for feeds without aspect=")
	}
	if y, err := exec.LookPath("yt-dlp"); err == nil {
		t.YtDlp = y
	}
	if tmpDir == "" {
		if tmpDir, err = os.MkdirTemp("", "camwall-"); err != nil {
			return nil, t, err
		}
	}
	t.TempDir = tmpDir
	fmt.Fprintf(os.Stderr, "Getting %d feed%s ready…\n", len(fs), plural(len(fs)))
	feeds.UniqueNames(fs)
	if err := feeds.Prepare(fs, t, 20*time.Second); err != nil {
		return nil, t, err
	}
	for i, f := range fs {
		if f.Note != "" {
			fmt.Fprintf(os.Stderr, "  feed %d (%s): %s\n", i+1, f.Name, f.Note)
		}
	}
	return fs, t, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func solve(fs []feeds.Feed) (*layout.Solved, error) {
	lf := make([]layout.Feed, len(fs))
	for i, f := range fs {
		lf[i] = layout.Feed{Aspect: f.Aspect, Weight: f.Weight}
	}
	start := time.Now()
	sv, err := layout.Solve(lf)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "Searched %s wall layouts in %v.\n", human(sv.Count()), time.Since(start).Round(time.Millisecond))
	return sv, nil
}

func human(n float64) string {
	if n < 1e6 {
		return strconv.FormatFloat(math.Round(n), 'f', -1, 64)
	}
	return strconv.FormatFloat(n, 'g', 3, 64)
}

// ---------- serve ----------

func serve(args []string) error {
	fs := newFlags("serve")
	var c common
	c.register(fs)
	addr := fs.String("addr", "127.0.0.1:7788", "")
	password := fs.String("password", "", "")
	title := fs.String("title", "camwall", "")
	fps := fs.Int("fps", 10, "")
	maxSide := fs.Int("max", 960, "")
	noOpen := fs.Bool("no-open", false, "")
	recDir := fs.String("record", "", "")
	keep := fs.String("keep", "7d", "")
	segment := fs.Duration("segment", 15*time.Minute, "")
	srcs, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *fps < 1 || *fps > 30 {
		return errors.New("-fps: 1 to 30")
	}
	feedsList, tools, err := c.load(srcs, true)
	if err != nil {
		return err
	}
	srv := wall.New(*title, tools, feedsList, *fps, *maxSide, 5)
	recCtx, stopRec := context.WithCancel(context.Background())
	defer stopRec()
	recDone := make(chan struct{})
	close(recDone)
	if *recDir != "" {
		rec, err := newRecorder(*recDir, *keep, *segment, tools, feedsList)
		if err != nil {
			return err
		}
		srv.Rec, srv.RecDir = rec, *recDir
		recDone = make(chan struct{})
		go func() { rec.Run(recCtx); close(recDone) }()
		defer func() { stopRec(); <-recDone }()
	}
	base := make([]float64, len(feedsList))
	all := make([]int, len(feedsList))
	for i, f := range feedsList {
		base[i], all[i] = f.Weight, i
	}
	if _, err := srv.Solve(all, base); err != nil {
		return err
	}
	h := srv.Handler()
	if *password != "" {
		h = basicAuth(h, *password)
	} else {
		h = localNamesOnly(h)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	url := "http://" + displayAddr(ln.Addr().String()) + "/"
	fmt.Fprintf(os.Stderr, "Camera wall at %s  (Ctrl-C to stop)\n", url)
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); host == "::" || host == "0.0.0.0" {
		if ip := lanIP(); ip != "" {
			_, port, _ := net.SplitHostPort(ln.Addr().String())
			fmt.Fprintf(os.Stderr, "Other devices on your network: http://%s:%s/\n", ip, port)
		}
		if *password == "" {
			fmt.Fprintln(os.Stderr, "Anyone on your network can watch; add -password to require one.")
		}
	}
	if !*noOpen {
		go openBrowser(url)
	}
	// every request's context ends at shutdown, so live streams and sound end too
	reqCtx, endRequests := context.WithCancel(context.Background())
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return reqCtx }}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		endRequests()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(ctx)
	}()
	err = hs.Serve(ln)
	if err != http.ErrServerClosed {
		endRequests()
		srv.Close()
		return err
	}
	<-stopped
	srv.Close() // stop every FFmpeg before exiting
	return nil
}

// parseKeep reads a retention period: 7d, 36h, 0 (keep everything).
func parseKeep(s string) (time.Duration, error) {
	if s == "0" || s == "" {
		return 0, nil
	}
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(d, 64)
		if err != nil || !(n >= 0 && n <= 100*365) { // also refuses NaN and Inf
			return 0, fmt.Errorf("-keep %q: want days like 7d (up to 100 years), hours like 36h, or 0", s)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	k, err := time.ParseDuration(s)
	if err != nil || k < 0 {
		return 0, fmt.Errorf("-keep %q: want days like 7d, hours like 36h, or 0", s)
	}
	return k, nil
}

// newRecorder sets up sound recording into dir and says what it will do.
func newRecorder(dir, keep string, segment time.Duration, tools feeds.Tools, fs []feeds.Feed) (*record.Recorder, error) {
	k, err := parseKeep(keep)
	if err != nil {
		return nil, err
	}
	if segment < 10*time.Second || segment > 24*time.Hour {
		return nil, errors.New("-segment: 10s to 24h")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	n := 0
	for _, f := range fs {
		if record.Wants(f) {
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "Recording sound: none of the feeds has sound.")
	} else {
		perDay := float64(n) * 48000 / 8 * 86400 / 1e9
		kept := "kept until you delete them"
		if k > 0 {
			gb := perDay * k.Hours() / 24
			size := fmt.Sprintf("%.0f GB", gb)
			if gb < 1 {
				size = fmt.Sprintf("%.0f MB", gb*1000)
			}
			kept = fmt.Sprintf("kept %s (about %s at most)", keep, size)
		}
		fmt.Fprintf(os.Stderr, "Recording the sound of %d feed%s into %s, in %v files, %s.\n", n, plural(n), dir, segment, kept)
	}
	return &record.Recorder{Dir: dir, Tools: tools, Feeds: fs, Segment: segment, Keep: k}, nil
}

// recordCmd records the feeds' sound without serving a wall.
func recordCmd(args []string) error {
	fs := newFlags("record")
	var c common
	c.register(fs)
	out := fs.String("o", "recordings", "")
	keep := fs.String("keep", "7d", "")
	segment := fs.Duration("segment", 15*time.Minute, "")
	srcs, err := parse(fs, args)
	if err != nil {
		return err
	}
	feedsList, tools, err := c.load(srcs, true)
	if err != nil {
		return err
	}
	rec, err := newRecorder(*out, *keep, *segment, tools, feedsList)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "(Ctrl-C to stop)")
	rec.Run(ctx)
	return nil
}

// localNamesOnly refuses requests addressed to a public host name. Without a
// password, that stops a web page elsewhere from pointing its own domain at
// this computer (DNS rebinding) and reading the cameras through a viewer's
// browser. IP addresses and local names (localhost, myhost, myhost.local)
// work as before.
func localNamesOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "camwall answers to IP addresses and local names only; start it with -password to use other names", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func localHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return true
	}
	for _, suf := range []string{".localhost", ".local", ".lan", ".home.arpa", ".internal"} {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	return false
}

func basicAuth(h http.Handler, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte("camwall"))
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(password))
		if !ok || userOK&passOK != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="camwall"`)
			http.Error(w, "password required", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func displayAddr(a string) string {
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return a
	}
	if host == "::" || host == "0.0.0.0" || host == "" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

func lanIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:9") // no packets are sent
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

// ---------- stream ----------

func parseSize(s string) (int, int, error) {
	a, b, ok := strings.Cut(strings.ToLower(s), "x")
	w, e1 := strconv.Atoi(a)
	h, e2 := strconv.Atoi(b)
	if !ok || e1 != nil || e2 != nil || w < 64 || h < 64 || w > 7680 || h > 4320 {
		return 0, 0, fmt.Errorf("-size %q: want WIDTHxHEIGHT, e.g. 1920x1080", s)
	}
	return w &^ 1, h &^ 1, nil
}

func stream(args []string) error {
	fs := newFlags("stream")
	var c common
	c.register(fs)
	out := fs.String("o", "wall.mkv", "")
	size := fs.String("size", "1920x1080", "")
	fps := fs.Int("fps", 15, "")
	gutter := fs.Int("gutter", 4, "")
	bg := fs.String("bg", "black", "")
	labels := fs.Bool("labels", false, "")
	font := fs.String("font", "", "")
	crf := fs.Int("crf", 23, "")
	dry := fs.Bool("dry-run", false, "")
	audio := fs.Int("audio", 0, "")
	encoder := fs.String("encoder", "libx264", "")
	bitrate := fs.String("bitrate", "", "")
	once := fs.Bool("once", false, "")
	srcs, err := parse(fs, args)
	if err != nil {
		return err
	}
	w, h, err := parseSize(*size)
	if err != nil {
		return err
	}
	if *fps < 1 || *fps > 60 {
		return errors.New("-fps: 1 to 60")
	}
	if *gutter < 0 || *gutter > 64 {
		return errors.New("-gutter: 0 to 64")
	}
	feedsList, tools, err := c.load(srcs, !*dry)
	if err != nil {
		return err
	}
	if *audio < 0 || *audio > len(feedsList) {
		return fmt.Errorf("-audio %d: feeds are numbered 1 to %d", *audio, len(feedsList))
	}
	if *audio > 0 && !feedsList[*audio-1].Audio {
		return fmt.Errorf("-audio %d: %s has no sound (or it could not be read)", *audio, feedsList[*audio-1].Name)
	}
	sv, err := solve(feedsList)
	if err != nil {
		return err
	}
	o := compose.Options{Width: w, Height: h, FPS: *fps, Gutter: *gutter, Background: *bg,
		Labels: *labels, Font: *font, Output: *out, CRF: *crf, Encoder: *encoder, Bitrate: *bitrate, Audio: *audio}
	wl := sv.Fit(float64(w) / float64(h))
	// web pages are resolved again on every start: their media URLs expire
	argsNow := func() ([]string, error) {
		opened := make([]feeds.Feed, len(feedsList))
		for i, f := range feedsList {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			g, err := f.Opened(ctx, tools)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("feed %d (%s): %w", i+1, f.Name, err)
			}
			opened[i] = g
		}
		o.Start = float64(time.Now().UnixMicro()) / 1e6
		return compose.Args(wl, opened, o), nil
	}
	if *dry {
		a, err := argsNow()
		if err != nil {
			return err
		}
		fmt.Println(compose.Quote("ffmpeg", a))
		return nil
	}
	if *out != "-" {
		fmt.Fprintf(os.Stderr, "Streaming the wall (%dx%d, %d fps) to %s  (Ctrl-C to stop)\n", w, h, *fps, *out)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backoff := 2 * time.Second
	for {
		start := time.Now()
		args, err := argsNow()
		if err != nil {
			fmt.Fprintf(os.Stderr, "camwall: %v; retrying in %v\n", err, backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, 30*time.Second)
			continue
		}
		cmd := feeds.Child(exec.Command(tools.FFmpeg, args...))
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-ctx.Done():
			// let FFmpeg finish the file properly
			cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
			}
			return nil
		case err := <-done:
			if *once || *out == "-" {
				return err
			}
			if time.Since(start) > time.Minute {
				backoff = 2 * time.Second
			}
			fmt.Fprintf(os.Stderr, "camwall: FFmpeg stopped (%v); restarting in %v\n", err, backoff)
			if isFile(*out) {
				fmt.Fprintln(os.Stderr, "camwall: note: restarting overwrites", *out, "(use -once to stop instead)")
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, 30*time.Second)
		}
	}
}

func isFile(out string) bool { return out != "-" && feeds.Scheme(out) == "" }

// ---------- plan ----------

func plan(args []string) error {
	fs := newFlags("plan")
	var c common
	c.register(fs)
	ratio := fs.String("ratio", "16:9", "")
	srcs, err := parse(fs, args)
	if err != nil {
		return err
	}
	screen, err := feeds.ParseRatio(*ratio)
	if err != nil {
		return err
	}
	feedsList, _, err := c.load(srcs, false)
	if err != nil {
		return err
	}
	sv, err := solve(feedsList)
	if err != nil {
		return err
	}
	wl := sv.Fit(screen)
	fmt.Print(sketch(wl, screen, 72))
	smallest := 1.0
	for _, t := range wl.Tiles {
		smallest = math.Min(smallest, t.W*t.H)
	}
	fmt.Printf("\nWall fills %.0f%% of a %s screen; the smallest feed gets %.1f%% of it.\n", 100*wl.Fill, *ratio, 100*smallest)
	if sv.Evened {
		fmt.Println("The sizes asked for were too varied to search quickly, so every feed is shown the same size.")
	}
	fmt.Println()
	for _, t := range wl.Tiles {
		f := feedsList[t.Feed]
		fmt.Printf("  %2d  %-28s %5.1f%% of screen  (%.2f:1%s)\n", t.Feed+1, trim(f.Name, 28), 100*t.W*t.H, f.Aspect, weightNote(f.Weight))
	}
	return nil
}

func weightNote(w float64) string {
	if w == 1 {
		return ""
	}
	return fmt.Sprintf(", weight %g", w)
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// sketch draws the wall in text, numbering each feed.
func sketch(wl layout.Wall, screen float64, cols int) string {
	rows := int(math.Round(float64(cols) / screen / 2.1)) // terminal cells are about twice as tall as wide
	rows = max(rows, 6)
	grid := make([][]rune, rows+1)
	for i := range grid {
		grid[i] = []rune(strings.Repeat(" ", cols+1))
	}
	put := func(y, x int, r rune) {
		if y >= 0 && y <= rows && x >= 0 && x <= cols {
			cur := grid[y][x]
			if (cur == '-' && r == '|') || (cur == '|' && r == '-') || cur == '+' {
				r = '+'
			}
			grid[y][x] = r
		}
	}
	for _, t := range wl.Tiles {
		x0, x1 := int(math.Round(t.X*float64(cols))), int(math.Round((t.X+t.W)*float64(cols)))
		y0, y1 := int(math.Round(t.Y*float64(rows))), int(math.Round((t.Y+t.H)*float64(rows)))
		for x := x0; x <= x1; x++ {
			put(y0, x, '-')
			put(y1, x, '-')
		}
		for y := y0; y <= y1; y++ {
			put(y, x0, '|')
			put(y, x1, '|')
		}
		for _, p := range [][2]int{{y0, x0}, {y0, x1}, {y1, x0}, {y1, x1}} {
			grid[p[0]][p[1]] = '+'
		}
		label := []rune(strconv.Itoa(t.Feed + 1))
		cy, cx := (y0+y1)/2, (x0+x1)/2-len(label)/2
		if y1-y0 >= 2 && x1-x0 > len(label)+1 {
			for i, r := range label {
				grid[cy][cx+i] = r
			}
		}
	}
	var b strings.Builder
	for _, row := range grid {
		b.WriteString(strings.TrimRight(string(row), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------- laws ----------

func laws() error {
	ok := true
	for _, r := range layout.AllLaws(500, 1) {
		mark := "ok"
		if !r.OK {
			mark, ok = "FAILED", false
		}
		fmt.Printf("  %-16s %-22s %s\n", r.Algebra, r.Law, mark)
	}
	fmt.Println()
	for n := 2; n <= 6; n++ {
		c := layout.VerifyDyna(n, int64(n))
		mark := "equal"
		if !c.OK {
			mark, ok = "DIFFERENT", false
		}
		fmt.Printf("  %d feeds, %d kinds  %7d walls by brute force  %4d states  %s\n", c.Feeds, c.Kinds, c.Trees, c.States, mark)
	}
	if !ok {
		return errors.New("a law failed")
	}
	return nil
}

// ---------- devices ----------

// devices lists the cameras and screens FFmpeg can capture on this computer.
func devices() error {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return errors.New("FFmpeg not found; install it, e.g. brew install ffmpeg")
	}
	switch runtime.GOOS {
	case "darwin":
		out, _ := exec.Command(ff, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
		fmt.Println("Use webcam:N for a camera and screen:N for a screen, numbered as below.")
		printDeviceLines(string(out))
	case "windows":
		out, _ := exec.Command(ff, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()
		fmt.Println(`Use "dshow:video=NAME" for a camera (in quotes in a feeds file); screen for the desktop.`)
		printDeviceLines(string(out))
	default:
		matches, _ := filepath.Glob("/dev/video*")
		if len(matches) == 0 {
			fmt.Println("No cameras found (/dev/video*).")
		}
		for _, m := range matches {
			fmt.Printf("  v4l2:%s   (or webcam:%s)\n", m, strings.TrimPrefix(m, "/dev/video"))
		}
		fmt.Println("Use screen for the X display ($DISPLAY).")
	}
	return nil
}

func printDeviceLines(out string) {
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "] "); i >= 0 && !strings.Contains(line, "Error") && !strings.Contains(line, "dummy") {
			fmt.Println("  " + strings.TrimSpace(line[i+2:]))
		}
	}
}

// ---------- discover ----------

// discover finds ONVIF cameras on the network and prints a feeds-file line
// for each, using the sub-stream when asked (lighter, plenty for a wall).
func discover(args []string) error {
	fs := newFlags("discover")
	user := fs.String("user", "", "")
	pass := fs.String("password", "", "")
	wait := fs.Duration("timeout", 3*time.Second, "")
	sub := fs.Bool("sub", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Looking for ONVIF cameras for %v…\n", *wait)
	ds, err := onvif.Discover(context.Background(), onvif.MulticastAddr, *wait)
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Fprintln(os.Stderr, "No cameras answered. Check that ONVIF is turned on in each camera's settings, and that this computer is on the same network.")
		return nil
	}
	fmt.Fprintf(os.Stderr, "Found %d. Lines for a feeds file (camwall serve -f cams.txt):\n\n", len(ds))
	c := &onvif.Client{User: *user, Password: *pass}
	authFailed := false
	for _, d := range ds {
		name := d.Name
		if name == "" {
			name = d.Addr
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		ss, err := c.Streams(ctx, d.XAddr)
		cancel()
		switch {
		case err == onvif.ErrAuth:
			authFailed = true
			fmt.Printf("# %s (%s %s): needs the camera's user name and password\n", name, d.Addr, d.Model)
		case err != nil:
			fmt.Printf("# %s (%s %s): %s\n", name, d.Addr, d.Model, onvif.Clean(feeds.Redact(err.Error())))
		case len(ss) == 0:
			fmt.Printf("# %s (%s): no RTSP streams listed\n", name, d.Addr)
		default:
			pick := ss[0]
			if *sub {
				pick = ss[len(ss)-1]
			}
			fmt.Printf("%s  name=%s\n", pick.URI, name)
			for _, s := range ss {
				if s.URI != pick.URI {
					fmt.Printf("#   also %s (%dx%d): %s\n", s.Profile, s.Width, s.Height, s.URI)
				}
			}
		}
	}
	if authFailed && *user == "" {
		fmt.Fprintln(os.Stderr, "\nSome cameras need a login: run again with -user NAME -password PW.")
	}
	return nil
}
