package feeds

import (
	"runtime"
	"strings"
	"testing"
)

func TestKindOf(t *testing.T) {
	for src, want := range map[string]Kind{
		"rtsp://user:pw@10.0.0.5:554/stream1": Live,
		"https://example.com/live/index.m3u8": Live,
		"srt://10.0.0.9:9000":                 Live,
		"lobby.mp4":                           Video,
		"/tmp/clip.MOV":                       Video,
		"promo.gif":                           GIF,
		"floor plan.PNG":                      Image,
		"https://example.com/logo.png":        Image,
		"file:///tmp/a.gif":                   GIF,
	} {
		if got := KindOf(src); got != want {
			t.Errorf("KindOf(%q) = %s, want %s", src, got, want)
		}
	}
}

func TestDefaultNameHidesCredentials(t *testing.T) {
	if n := DefaultName("rtsp://admin:secret@192.168.1.20:554/Streaming/101"); n != "192.168.1.20/Streaming/101" || strings.Contains(n, "secret") {
		t.Errorf("got %q", n)
	}
	if n := DefaultName("/videos/front door.mp4"); n != "front door" {
		t.Errorf("got %q", n)
	}
}

func TestRead(t *testing.T) {
	in := `# shop
rtsp://192.168.1.20/s1  name=Front door  weight=2
rtsp://192.168.1.21/s1 weight=1.5 aspect=4:3 name=Register  (till 2)

floor.png aspect=1200x800
`
	fs, err := Read(strings.NewReader(in), "cams.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 3 {
		t.Fatalf("got %d feeds", len(fs))
	}
	if fs[0].Name != "Front door" || fs[0].Weight != 2 {
		t.Errorf("feed 1: name %q weight %v", fs[0].Name, fs[0].Weight)
	}
	if fs[1].Weight != 1.5 || fs[1].Aspect != 4.0/3 || fs[1].Name != "Register (till 2)" || !fs[1].fixed {
		t.Errorf("feed 2: %+v", fs[1])
	}
	if fs[2].Aspect != 1.5 || fs[2].Kind != Image || fs[2].Name != "floor" {
		t.Errorf("feed 3: %+v", fs[2])
	}
	for _, bad := range []string{"cam.mp4 weight=0", "cam.mp4 weight=x", "cam.mp4 aspect=0", "cam.mp4 size=3", "cam.mp4 junk", "cam.mp4 name="} {
		if _, err := Read(strings.NewReader(bad), "x"); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestInputArgs(t *testing.T) {
	cam := New("rtsp://10.0.0.5/s")
	a := strings.Join(cam.WallclockInputArgs(15), " ")
	for _, want := range []string{"-use_wallclock_as_timestamps 1", "-rtsp_flags prefer_tcp", "-i rtsp://10.0.0.5/s"} {
		if !strings.Contains(a, want) {
			t.Errorf("camera args %q lack %q", a, want)
		}
	}
	gif := strings.Join(New("a.gif").WallclockInputArgs(15), " ")
	if strings.Contains(gif, "wallclock") || !strings.Contains(gif, "-re -ignore_loop 0 -i a.gif") {
		t.Errorf("gif args %q", gif)
	}
	img := strings.Join(New("a.png").InputArgs(10), " ")
	if !strings.Contains(img, "-re -loop 1 -framerate 10 -i a.png") {
		t.Errorf("image args %q", img)
	}
}

func TestSources(t *testing.T) {
	for _, tc := range []struct {
		src           string
		kind          Kind
		format, input string
		web           bool
	}{
		{"v4l2:/dev/video2", Live, "v4l2", "/dev/video2", false},
		{"video4linux2:/dev/video0", Live, "v4l2", "/dev/video0", false},
		{"avfoundation:1:none", Live, "avfoundation", "1:none", false},
		{"dshow:video=Logitech C920", Live, "dshow", "video=Logitech C920", false},
		{"decklink:DeckLink Mini Recorder", Live, "decklink", "DeckLink Mini Recorder", false},
		{"lavfi:testsrc2=size=640x480", Video, "lavfi", "testsrc2=size=640x480", false},
		{"https://www.youtube.com/watch?v=x", Live, "", "https://www.youtube.com/watch?v=x", true},
		{"https://youtu.be/x", Live, "", "https://youtu.be/x", true},
		{"https://www.twitch.tv/somebody", Live, "", "https://www.twitch.tv/somebody", true},
		{"ytdl:https://example.com/page", Live, "", "https://example.com/page", true},
		{"https://example.com/cam.mjpg", Live, "", "https://example.com/cam.mjpg", false},
		{"https://example.com/clip.mkv", Video, "", "https://example.com/clip.mkv", false},
		{"photo.HEIC", Image, "", "photo.HEIC", false},
		{"scan.tiff", Image, "", "scan.tiff", false},
		{"C:\\videos\\shop.mp4", Video, "", "C:\\videos\\shop.mp4", false},
	} {
		f := New(tc.src)
		if f.Kind != tc.kind || f.Format != tc.format || f.Input != tc.input || f.Web != tc.web {
			t.Errorf("New(%q) = kind %s format %q input %q web %v", tc.src, f.Kind, f.Format, f.Input, f.Web)
		}
	}
	if w := New("webcam"); w.Kind != Live || w.Format == "" || w.Name != "Camera 0" {
		t.Errorf("webcam: %+v", w)
	}
	if s := New("screen:1"); s.Kind != Live || s.Format == "" || s.Name != "Screen 1" {
		t.Errorf("screen:1: %+v", s)
	}
	if New("dshow:video=X").IsFile() || New("webcam:2").IsFile() || !New("a/b.mp4").IsFile() || !New("C:\\x.mp4").IsFile() {
		t.Error("IsFile")
	}
}

func TestOptions(t *testing.T) {
	fs, err := Read(strings.NewReader(`"dshow:video=Integrated Camera" size=1280x720 fps=30 name=Laptop camera
rtsp://10.0.0.9/live transport=udp filter=crop=iw/2:ih:0:0 name=Left half weight=2
v4l2:/dev/video0 format=mjpeg name=USB cam`), "x")
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Join(fs[0].InputArgs(10), " ")
	if !strings.Contains(a, "-f dshow -video_size 1280x720 -framerate 30 -i video=Integrated Camera") || fs[0].Name != "Laptop camera" {
		t.Errorf("device args %q name %q", a, fs[0].Name)
	}
	b := strings.Join(fs[1].InputArgs(10), " ")
	if !strings.Contains(b, "-rtsp_transport udp") || fs[1].Filter != "crop=iw/2:ih:0:0" || fs[1].Weight != 2 || fs[1].Name != "Left half" {
		t.Errorf("udp/filter: %q %+v", b, fs[1])
	}
	if fs[2].Format != "mjpeg" {
		t.Errorf("format= did not override: %+v", fs[2])
	}
	for _, bad := range []string{"x.mp4 size=big", "x.mp4 fps=0", "x.mp4 transport=quic", "x.mp4 filter=", `"unclosed x.mp4`} {
		if _, err := Read(strings.NewReader(bad), "x"); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestUniqueNamesTabsDisplay(t *testing.T) {
	fs := []Feed{{Name: "Door"}, {Name: "Yard"}, {Name: "door"}, {Name: "Door 2"}, {Name: "Door"}}
	UniqueNames(fs)
	got := []string{}
	for _, f := range fs {
		got = append(got, f.Name)
	}
	if strings.Join(got, "|") != "Door|Yard|door 3|Door 2|Door 4" {
		t.Errorf("UniqueNames: %v", got)
	}
	f, ok, err := ParseLine("rtsp://10.0.0.5/live\tweight=2\tname=Front door")
	if !ok || err != nil || f.Weight != 2 || f.Name != "Front door" {
		t.Errorf("tabs: %+v %v", f, err)
	}
	t.Setenv("DISPLAY", ":1.0")
	if g := New("screen"); runtime.GOOS == "linux" && g.Input != ":1.0" {
		t.Errorf("DISPLAY: %q", g.Input)
	}
}
