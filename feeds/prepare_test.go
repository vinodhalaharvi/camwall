package feeds

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAnimatedAndWebPSize(t *testing.T) {
	dir := t.TempDir()
	apng := filepath.Join(dir, "a.png")
	os.WriteFile(apng, append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x08acTL\x00\x00\x00\x02\x00\x00\x00\x00"), []byte("IDAT")...), 0o644)
	png := filepath.Join(dir, "b.png")
	os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR....IDAT"), 0o644)
	if f, ok := animated(apng); !ok || f != "apng" {
		t.Errorf("animated PNG not found: %q %v", f, ok)
	}
	if _, ok := animated(png); ok {
		t.Error("plain PNG taken for an animation")
	}
	// VP8X header: canvas 320×240 (stored minus one, little-endian, 24 bits)
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x12\x00\x00\x00\x3f\x01\x00\xef\x00\x00ANIM")
	wp := filepath.Join(dir, "c.webp")
	os.WriteFile(wp, webp, 0o644)
	if f, ok := animated(wp); !ok || f != "webp" {
		t.Errorf("animated WebP not found")
	}
	if w, h := webpSize(wp); w != 320 || h != 240 {
		t.Errorf("webpSize = %dx%d", w, h)
	}
}

func TestOpenedWithYtDlp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "yt-dlp")
	os.WriteFile(fake, []byte("#!/bin/sh\necho True\necho https://cdn.example/live/index.m3u8\n"), 0o755)
	f := New("https://www.youtube.com/watch?v=live")
	g, err := f.Opened(context.Background(), Tools{YtDlp: fake})
	if err != nil || g.Input != "https://cdn.example/live/index.m3u8" || g.Kind != Live || f.Input == g.Input {
		t.Errorf("Opened: %+v %v", g, err)
	}
	if _, err := f.Opened(context.Background(), Tools{}); err != ErrNoYtDlp {
		t.Errorf("without yt-dlp: %v", err)
	}
	if g, _ := New("rtsp://x/y").Opened(context.Background(), Tools{}); g.Input != "rtsp://x/y" {
		t.Error("non-web feed changed")
	}
}

// TestPreparePictures needs FFmpeg; it is skipped where FFmpeg is missing.
func TestPreparePictures(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	probe, _ := exec.LookPath("ffprobe")
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command(ff, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %s", args, out)
		}
	}
	run("-f", "lavfi", "-i", "testsrc2=size=300x200", "-frames:v", "1", filepath.Join(dir, "s.tiff"))
	run("-t", "1", "-f", "lavfi", "-i", "testsrc2=size=160x120:rate=5", "-plays", "0", "-f", "apng", filepath.Join(dir, "a.png"))
	run("-t", "1", "-f", "lavfi", "-i", "testsrc2=size=640x160:rate=10", filepath.Join(dir, "wide.mp4"))
	fs := []Feed{New(filepath.Join(dir, "s.tiff")), New(filepath.Join(dir, "a.png")), New(filepath.Join(dir, "wide.mp4")), New("lavfi:testsrc2=size=400x400")}
	fs[2].Filter = "crop=iw/2:ih:0:0"
	if err := Prepare(fs, Tools{FFmpeg: ff, FFprobe: probe, TempDir: dir}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if fs[0].Browser == "" || filepath.Ext(fs[0].Browser) != ".jpg" || fs[0].Aspect != 1.5 {
		t.Errorf("TIFF: %+v", fs[0])
	}
	if fs[1].Kind != GIF || fs[1].Format != "apng" || fs[1].Browser != fs[1].Src {
		t.Errorf("APNG: %+v", fs[1])
	}
	if fs[2].Aspect != 2 {
		t.Errorf("filtered video: aspect %v, want 2 (half of 4:1)", fs[2].Aspect)
	}
	if fs[3].Aspect != 1 {
		t.Errorf("lavfi: aspect %v", fs[3].Aspect)
	}
}
