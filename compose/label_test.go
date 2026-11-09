package compose

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/camwall/feeds"
)

// Names with quotes, percent signs and filter punctuation reach FFmpeg intact.
func TestAwkwardLabels(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	for _, name := range []string{`Bob's 50% cam`, `a, b; [c] d=e`, `back\slash: "q" %{pts}`, `Café ☕`} {
		f := feeds.New("x.mp4")
		f.Name = name
		g := "color=s=320x240:d=0.1" + strings.SplitN(Filter([]Rect{{0, 0, 320, 240}}, []feeds.Feed{f}, Options{Width: 320, Height: 240, FPS: 5, Labels: true, Background: "black"}), "format=yuv420p", 2)[1]
		g = g[:strings.Index(g, "[v0]")]
		out, err := exec.Command(ff, "-hide_banner", "-loglevel", "warning", "-f", "lavfi", "-i", g, "-f", "null", "-").CombinedOutput()
		if err != nil || len(out) > 0 {
			t.Errorf("%q: %v %s\n%s", name, err, out, g)
		}
	}
}
