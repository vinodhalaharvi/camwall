package feeds

import (
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
	for _, want := range []string{"-use_wallclock_as_timestamps 1", "-rtsp_transport tcp", "-i rtsp://10.0.0.5/s"} {
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
