package record

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinodhalaharvi/camwall/feeds"
)

func TestFolderName(t *testing.T) {
	for in, want := range map[string]string{
		"Front door (mic)":    "03 Front door (mic)",
		"../../etc/passwd":    "03 _.._etc_passwd",
		"Caisse n°2 / entrée": "03 Caisse n_2 _ entrée",
	} {
		if got := FolderName(2, in); got != want {
			t.Errorf("FolderName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Path(dir, "01 Front door", "2026-11-08_14-15-00.m4a"); !ok {
		t.Error("a real recording was refused")
	}
	for _, bad := range [][2]string{{"..", "2026-11-08_14-15-00.m4a"}, {"01 x", "../../secret"}, {"a/b", "2026-11-08_14-15-00.m4a"},
		{"01 x", "notes.txt"}, {"", "2026-11-08_14-15-00.m4a"}} {
		if _, ok := Path(dir, bad[0], bad[1]); ok {
			t.Errorf("Path(%q, %q) allowed", bad[0], bad[1])
		}
	}
}

func TestListAndPrune(t *testing.T) {
	dir := t.TempDir()
	fd := filepath.Join(dir, "01 Front door")
	os.MkdirAll(fd, 0o755)
	now := time.Date(2026, 11, 8, 14, 20, 0, 0, time.Local)
	for _, n := range []string{"2026-11-08_14-15-00.m4a", "2026-11-08_14-00-00.m4a", "2026-11-01_09-00-00.m4a", "stray.txt"} {
		os.WriteFile(filepath.Join(fd, n), []byte("x"), 0o644)
	}
	ls, err := List(dir)
	if err != nil || len(ls) != 1 || len(ls[0].Files) != 3 || ls[0].Files[0].Name != "2026-11-08_14-15-00.m4a" {
		t.Fatalf("List: %+v %v", ls, err)
	}
	r := &Recorder{Dir: dir, Keep: 2 * 24 * time.Hour, Segment: 15 * time.Minute}
	r.Prune(now)
	ls, _ = List(dir)
	if len(ls[0].Files) != 2 {
		t.Errorf("after pruning 2 days: %+v", ls[0].Files)
	}
	if _, err := os.Stat(filepath.Join(fd, "stray.txt")); err != nil {
		t.Error("prune removed a file that is not a recording")
	}
}

func TestArgsAndWants(t *testing.T) {
	cam := feeds.New("rtsp://10.0.0.5/live")
	cam.Audio = true
	r := &Recorder{Segment: 15 * time.Minute, Bitrate: "48k"}
	a := strings.Join(r.Args(cam, "/rec/01 Front door"), " ")
	for _, want := range []string{"-map 0:a:0 -vn", "-segment_time 900 -segment_atclocktime 1", "flush_packets=1", "/rec/01 Front door/" + namePattern() + ""} {
		if !strings.Contains(a, want) {
			t.Errorf("args lack %q: %s", want, a)
		}
	}
	pic := feeds.New("logo.png")
	offline := feeds.New("rtsp://10.0.0.6/live")
	offline.Note = "could not read its size"
	if !Wants(cam) || Wants(pic) || !Wants(offline) {
		t.Error("Wants")
	}
}

func TestRepeatedHourAndPruneScope(t *testing.T) {
	// the hour clocks go back: same local time, different offsets
	a, okA := startOf("2026-11-01_01-30-00-0400.m4a")
	b, okB := startOf("2026-11-01_01-30-00-0500.m4a")
	if !okA || !okB || b.Sub(a) != time.Hour {
		t.Fatalf("offsets: %v %v %v", a, b, b.Sub(a))
	}
	if _, ok := startOf("2026-11-01_01-30-00.m4a"); !ok {
		t.Error("older names without an offset must still be read")
	}
	dir := t.TempDir()
	other := filepath.Join(dir, "photos", "deep")
	os.MkdirAll(other, 0o755)
	old := filepath.Join(other, "2020-01-01_00-00-00.m4a")
	os.WriteFile(old, []byte("x"), 0o644)
	r := &Recorder{Dir: dir, Keep: time.Hour, Segment: time.Minute}
	r.Prune(time.Now())
	if _, err := os.Stat(old); err != nil {
		t.Error("prune touched a file outside camwall's feed folders")
	}
}
