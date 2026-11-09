package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The demo's pet clips: short, freely licensed loops (see CREDITS.txt in the
// archive), downloaded once and kept in the user's cache folder.
const (
	demoURL    = "https://raw.githubusercontent.com/vinodhalaharvi/camwall/demo-clips/camwall-demo-clips-1.tar.gz"
	demoSHA256 = "68bc2efb97589dfb639081006de42977f788c4d1fc291e4a8e8f252ddf76f2ac"
	demoDir    = "demo-clips-1"
)

var demoFeeds = []string{
	"puppy-room.mp4 name=Puppy room",
	"back-yard.mp4  name=Back yard",
	"fence-cam.mp4  name=Fence",
	"hallway.mp4    name=Hallway",
	"kitten-nap.mp4 name=Nap spot",
	"driveway.mp4   name=Driveway",
	"shop-cat.jpg   name=Shop cat",
}

// Without the clips (offline), FFmpeg's own moving test pictures stand in.
var patternFeeds = []string{
	"lavfi:testsrc2=s=640x360:r=10 name=Test card",
	"lavfi:mandelbrot=s=480x480:r=10 name=Fractal",
	"lavfi:life=s=320x180:r=8:mold=10:ratio=0.1:life_color=#ffb347:death_color=#202830 name=Life",
	"lavfi:smptehdbars=s=640x360:r=5 name=Colour bars",
	"lavfi:cellauto=s=360x480:r=10:rule=110 name=Rule 110",
	"lavfi:testsrc=s=480x360:r=10 name=Clock",
}

// demo opens the wall with pet clips playing as cameras, so camwall can be
// tried with no cameras. Any serve flags (-addr, -no-open, …) pass through.
func demo(args []string) error {
	// "camwall demo ." and similar: ignore stray non-flag arguments
	var pass []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || (len(pass) > 0 && strings.HasPrefix(pass[len(pass)-1], "-") && !strings.Contains(pass[len(pass)-1], "=") && !isBoolFlag(pass[len(pass)-1])) {
			pass = append(pass, a)
		}
	}
	lines, dir, err := demoClips()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not fetch the demo's pet clips (%v);\nshowing FFmpeg's test pictures instead.\n", err)
		lines, dir = patternFeeds, ""
	} else {
		fmt.Fprintln(os.Stderr, "Pet clips from Wikimedia Commons, played as if they were cameras (credits:", filepath.Join(dir, "CREDITS.txt")+").")
	}
	var b strings.Builder
	for _, l := range lines {
		if dir != "" {
			src, rest, _ := strings.Cut(l, " ")
			l = quoteIfSpaced(filepath.Join(dir, src)) + " " + rest
		}
		b.WriteString(l + "\n")
	}
	f, err := os.CreateTemp("", "camwall-demo-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(b.String()); err != nil {
		return err
	}
	f.Close()
	return serve(append([]string{"-f", f.Name(), "-title", "camwall demo"}, pass...))
}

func isBoolFlag(f string) bool {
	switch strings.TrimLeft(f, "-") {
	case "no-open":
		return true
	}
	return false
}

func quoteIfSpaced(p string) string {
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

// demoClips returns the demo's feed lines and the folder holding the clips,
// downloading them first if needed.
func demoClips() ([]string, string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Join(cache, "camwall", demoDir)
	if _, err := os.Stat(filepath.Join(dir, "CREDITS.txt")); err == nil {
		return demoFeeds, dir, nil
	}
	fmt.Fprintln(os.Stderr, "Downloading the demo's pet clips (3.5 MB, once)…")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", demoURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", errors.New(resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != demoSHA256 {
		return nil, "", errors.New("the download did not match (checksum)")
	}
	tmp := dir + ".part"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, "", err
	}
	if err := untar(data, tmp); err != nil {
		os.RemoveAll(tmp)
		return nil, "", err
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return nil, "", err
	}
	return demoFeeds, dir, nil
}

// untar writes the archive's regular files, by base name only, into dir.
func untar(data []byte, dir string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(h.Name)
		if name == "." || name == ".." || strings.HasPrefix(name, ".") {
			continue
		}
		out, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, 50<<20))
		out.Close()
		if err != nil {
			return err
		}
	}
}
