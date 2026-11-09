package compose

import (
	"math"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/camwall/feeds"
	"github.com/vinodhalaharvi/camwall/layout"
)

func wallOf(t *testing.T, aspects []float64, screen float64) (layout.Wall, []feeds.Feed) {
	t.Helper()
	lf := make([]layout.Feed, len(aspects))
	fs := make([]feeds.Feed, len(aspects))
	for i, a := range aspects {
		lf[i] = layout.Feed{Aspect: a, Weight: 1}
		fs[i] = feeds.New("rtsp://10.0.0.1/cam" + string(rune('a'+i)))
		fs[i].Aspect = a
	}
	sv, err := layout.Solve(lf)
	if err != nil {
		t.Fatal(err)
	}
	return sv.Fit(screen), fs
}

func TestPlace(t *testing.T) {
	aspects := []float64{16.0 / 9, 16.0 / 9, 16.0 / 9, 4.0 / 3, 4.0 / 3, 1, 9.0 / 16}
	for _, o := range []Options{
		{Width: 1920, Height: 1080, Gutter: 4},
		{Width: 1280, Height: 720, Gutter: 0},
		{Width: 1080, Height: 1920, Gutter: 6},
	} {
		w, fs := wallOf(t, aspects, float64(o.Width)/float64(o.Height))
		rs := Place(w, fs, o)
		for i, r := range rs {
			if r.X%2 != 0 || r.Y%2 != 0 || r.W%2 != 0 || r.H%2 != 0 {
				t.Errorf("%dx%d feed %d: odd rectangle %+v", o.Width, o.Height, i, r)
			}
			if r.X < 0 || r.Y < 0 || r.X+r.W > o.Width || r.Y+r.H > o.Height {
				t.Errorf("%dx%d feed %d: off screen %+v", o.Width, o.Height, i, r)
			}
			if got := float64(r.W) / float64(r.H); math.Abs(got-aspects[i])/aspects[i] > 0.03 {
				t.Errorf("%dx%d feed %d: shape %.3f, feed is %.3f", o.Width, o.Height, i, got, aspects[i])
			}
			for j, q := range rs[i+1:] {
				if r.X < q.X+q.W && q.X < r.X+r.W && r.Y < q.Y+q.H && q.Y < r.Y+r.H {
					t.Errorf("%dx%d feeds %d and %d overlap", o.Width, o.Height, i, i+1+j)
				}
			}
		}
	}
}

func TestArgs(t *testing.T) {
	w, fs := wallOf(t, []float64{16.0 / 9, 4.0 / 3}, 16.0/9)
	fs = append(fs, feeds.New("promo.gif"))
	fs[2].Aspect = 1
	w2, _ := layout.Solve([]layout.Feed{{Aspect: 16.0 / 9, Weight: 1}, {Aspect: 4.0 / 3, Weight: 1}, {Aspect: 1, Weight: 1}})
	w = w2.Fit(16.0 / 9)
	o := Options{Width: 1280, Height: 720, FPS: 15, Gutter: 4, Background: "black", Output: "-", CRF: 23, Start: 1700000000.5}
	a := strings.Join(Args(w, fs, o), " ")
	for _, want := range []string{
		"-copyts",
		"-use_wallclock_as_timestamps 1 -thread_queue_size 64 -analyzeduration",
		"setpts='if(gt(PTS*TB,1e9),PTS-1700000000.500000/TB,(RTCTIME/1000000-1700000000.500000)/TB)',fps=15",
		"setpts=PTS-STARTPTS+(RTCSTART/1000000-1700000000.500000)/TB",
		"color=c=black:s=1280x720:r=15",
		"-f mpegts pipe:1",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("args lack %q:\n%s", want, a)
		}
	}
	for out, want := range map[string]string{"-": "mpegts", "rtsp://h/wall": "rtsp", "rtmp://h/live/x": "flv",
		"srt://h:9000": "mpegts", "wall.m3u8": "hls", "wall.ts": "mpegts", "wall.mp4": ""} {
		if got := OutputFormat(out); got != want {
			t.Errorf("OutputFormat(%q) = %q, want %q", out, got, want)
		}
	}
}

func TestQuote(t *testing.T) {
	got := Quote("ffmpeg", []string{"-i", "rtsp://a:b@h/s", "-filter_complex", "[0:v]scale=2:2[o]", "it's"})
	want := `ffmpeg -i rtsp://a:b@h/s -filter_complex '[0:v]scale=2:2[o]' 'it'\''s'`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestAudioEncoderFilter(t *testing.T) {
	sv, _ := layout.Solve([]layout.Feed{{Aspect: 16.0 / 9, Weight: 1}, {Aspect: 16.0 / 9, Weight: 1}})
	fs := []feeds.Feed{feeds.New("rtsp://h/a"), feeds.New("rtsp://h/b")}
	fs[0].Aspect, fs[1].Aspect = 16.0/9, 16.0/9
	fs[1].Filter = "hflip"
	o := Options{Width: 1280, Height: 720, FPS: 10, Background: "black", Output: "x.mkv", CRF: 23, Audio: 2,
		Encoder: "h264_videotoolbox", Bitrate: "8M", Start: 1}
	a := strings.Join(Args(sv.Fit(16.0/9), fs, o), " ")
	for _, want := range []string{"[1:a]asetpts='if(gt(PTS*TB,1e9),PTS-1.000000/TB,(RTCTIME/1000000-1.000000)/TB)',aresample", "-map [aout] -c:a aac", "-c:v h264_videotoolbox -b:v 8M",
		"fps=10,hflip,scale="} {
		if !strings.Contains(a, want) {
			t.Errorf("args lack %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, " -an ") || strings.Contains(a, "-pix_fmt yuv420p") {
		t.Errorf("unexpected -an or -pix_fmt with videotoolbox:\n%s", a)
	}
	o.Audio, o.Encoder = 0, ""
	if a := strings.Join(Args(sv.Fit(16.0/9), fs, o), " "); !strings.Contains(a, " -an ") || !strings.Contains(a, "libx264") {
		t.Errorf("default: %s", a)
	}
}

func TestPlatformOutput(t *testing.T) {
	sv, _ := layout.Solve([]layout.Feed{{Aspect: 16.0 / 9, Weight: 1}})
	fs := []feeds.Feed{feeds.New("rtsp://h/a")}
	fs[0].Aspect = 16.0 / 9
	o := Options{Width: 1920, Height: 1080, FPS: 30, Background: "black", Output: "rtmp://a.rtmp.youtube.com/live2/KEY", CRF: 23, Start: 1}
	a := strings.Join(Args(sv.Fit(16.0/9), fs, o), " ")
	for _, want := range []string{"-f lavfi -i anullsrc=channel_layout=stereo:sample_rate=44100", "-map 1:a -c:a aac",
		"-b:v 6M -minrate 6M -maxrate 6M -bufsize 6M", "nal-hrd=cbr", "-g 60", "-f flv rtmp://"} {
		if !strings.Contains(a, want) {
			t.Errorf("YouTube args lack %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, "-crf") || strings.Contains(a, " -an ") {
		t.Errorf("YouTube args should be CBR with sound:\n%s", a)
	}
	o.Audio = 1
	if a := strings.Join(Args(sv.Fit(16.0/9), fs, o), " "); strings.Contains(a, "anullsrc") || !strings.Contains(a, "-map [aout]") {
		t.Errorf("with a feed's sound, no silent track:\n%s", a)
	}
	o.Audio, o.Output = 0, "wall.mkv"
	if a := strings.Join(Args(sv.Fit(16.0/9), fs, o), " "); strings.Contains(a, "anullsrc") || !strings.Contains(a, "-crf 23") {
		t.Errorf("a file keeps quality-based encoding:\n%s", a)
	}
}
