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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vinodhalaharvi/camwall/compose"
	"github.com/vinodhalaharvi/camwall/feeds"
	"github.com/vinodhalaharvi/camwall/layout"
	"github.com/vinodhalaharvi/camwall/wall"
)

var version = "dev"

const usage = `camwall puts every camera feed on one gap-free wall.

Usage:
  camwall serve  [flags] SOURCE...   watch the wall in a browser
  camwall stream [flags] SOURCE...   combine the feeds into one video with FFmpeg
  camwall plan   [flags] SOURCE...   print the wall for a screen shape
  camwall laws                       verify the layout algebra against brute force
  camwall version

A SOURCE is a camera or stream URL (rtsp://, rtmp://, srt://, udp://, http(s)://
including HLS .m3u8), a video file (looped), a GIF, or a still image. Instead of
listing sources, pass -f FILE with one source per line:

  rtsp://user:pass@192.168.1.20/stream1  name=Front door  weight=2
  rtsp://192.168.1.21/stream1            name=Register
  floorplan.png                          aspect=4:3 name=Floor plan

weight=N asks for N times the area of the other feeds, aspect=W:H skips probing,
and name= takes the rest of the line.

Flags for all commands:
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

stream flags:
  -o DEST          file (.mp4 .mkv .ts .m3u8), URL (udp:// srt:// rtsp:// rtmp://),
                   or - for MPEG-TS on stdout (default wall.mkv)
  -size WxH        output size (default 1920x1080)
  -fps N           frame rate (default 15)
  -gutter PX       space between feeds (default 4)
  -bg COLOR        background (default black)
  -labels          draw each feed's name (needs FFmpeg built with drawtext)
  -crf N           x264 quality, lower is better (default 23)
  -dry-run         print the FFmpeg command instead of running it
  -once            don't restart FFmpeg when it stops

plan flags:
  -ratio R         screen shape: 16:9, 21:9, 9:16, 1.6 … (default 16:9)

Examples:
  camwall serve rtsp://192.168.1.20/stream1 rtsp://192.168.1.21/stream1 logo.png
  camwall serve -f cams.txt -addr :7788 -password s3cret
  camwall stream -f cams.txt -o - | vlc -
  camwall stream -f cams.txt -o rtsp://localhost:8554/wall
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
	default:
		err = fmt.Errorf("unknown command %q (try camwall help)", args[0])
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

// load reads the sources, finds FFmpeg and probes every feed's shape.
func (c *common) load(srcs []string, needFFmpeg bool) ([]feeds.Feed, string, error) {
	var fs []feeds.Feed
	if c.file != "" {
		list, err := feeds.ReadFile(c.file)
		if err != nil {
			return nil, "", err
		}
		fs = append(fs, list...)
	}
	for _, s := range srcs {
		fs = append(fs, feeds.New(s))
	}
	if len(fs) == 0 {
		return nil, "", errors.New("no sources: list camera URLs or files, or use -f FILE (see camwall help)")
	}
	if len(fs) > layout.MaxFeeds {
		return nil, "", fmt.Errorf("%d sources; one wall takes at most %d (run a second wall for the rest)", len(fs), layout.MaxFeeds)
	}
	for _, f := range fs {
		if feeds.Scheme(f.Src) == "" {
			if _, err := os.Stat(f.Src); err != nil {
				return nil, "", fmt.Errorf("%s: %w", f.Src, err)
			}
		}
	}
	ff, err := exec.LookPath(c.ffmpeg)
	if err != nil {
		if needFFmpeg {
			return nil, "", fmt.Errorf("FFmpeg not found (%s); install it, e.g. brew install ffmpeg", c.ffmpeg)
		}
		ff = c.ffmpeg
	}
	probe := "ffprobe"
	if dir := strings.TrimSuffix(ff, "ffmpeg"); dir != ff {
		if _, err := os.Stat(dir + "ffprobe"); err == nil {
			probe = dir + "ffprobe"
		}
	}
	if _, err := exec.LookPath(probe); err != nil {
		fmt.Fprintln(os.Stderr, "camwall: ffprobe not found; assuming 16:9 for feeds without aspect=")
		for i := range fs {
			if !(fs[i].Aspect > 0) {
				fs[i].Aspect, fs[i].Note = 16.0/9, "ffprobe not found; assuming 16:9"
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "Reading the size of %d feed%s…\n", len(fs), plural(len(fs)))
		feeds.ProbeAll(probe, fs, 12*time.Second)
	}
	for i, f := range fs {
		if f.Note != "" {
			fmt.Fprintf(os.Stderr, "  feed %d (%s): %s\n", i+1, f.Name, f.Note)
		}
	}
	return fs, ff, nil
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
	srcs, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *fps < 1 || *fps > 30 {
		return errors.New("-fps: 1 to 30")
	}
	feedsList, ff, err := c.load(srcs, true)
	if err != nil {
		return err
	}
	srv := wall.New(*title, ff, feedsList, *fps, *maxSide, 5)
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
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(ctx)
	}()
	if err := hs.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func basicAuth(h http.Handler, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(p), []byte(password)) != 1 {
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
	feedsList, ff, err := c.load(srcs, !*dry)
	if err != nil {
		return err
	}
	sv, err := solve(feedsList)
	if err != nil {
		return err
	}
	o := compose.Options{Width: w, Height: h, FPS: *fps, Gutter: *gutter, Background: *bg,
		Labels: *labels, Font: *font, Output: *out, CRF: *crf}
	wl := sv.Fit(float64(w) / float64(h))
	argsNow := func() []string {
		o.Start = float64(time.Now().UnixMicro()) / 1e6
		return compose.Args(wl, feedsList, o)
	}
	if *dry {
		fmt.Println(compose.Quote("ffmpeg", argsNow()))
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
		cmd := exec.Command(ff, argsNow()...)
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
	fmt.Printf("\nWall fills %.0f%% of a %s screen; the smallest feed gets %.1f%% of it.\n\n", 100*wl.Fill, *ratio, 100*wl.Smallest)
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
