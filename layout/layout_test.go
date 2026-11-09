package layout

import (
	"math"
	"math/bits"
	"math/rand"
	"testing"
	"time"
)

func TestLaws(t *testing.T) {
	for _, r := range AllLaws(300, 1) {
		if !r.OK {
			t.Errorf("%s: %s failed", r.Algebra, r.Law)
		}
	}
}

func TestDynaEqualsBruteForce(t *testing.T) {
	for n := 1; n <= 6; n++ {
		for seed := int64(0); seed < 4; seed++ {
			if c := VerifyDyna(n, seed); !c.OK {
				t.Errorf("n=%d seed=%d: dynamorphism differs from brute force (%+v)", n, seed, c)
			}
		}
	}
}

// exhaustive is the exact answer: every labelled guillotine wall of the
// feeds, scored with exact aspects, no buckets and no grouping.
func exhaustive(feeds []Feed, screen float64) float64 {
	type res struct{ a, s float64 }
	memo := map[uint32][]res{}
	var all func(m uint32) []res
	all = func(m uint32) []res {
		if v, ok := memo[m]; ok {
			return v
		}
		var out []res
		if m&(m-1) == 0 {
			f := feeds[bits.TrailingZeros32(m)]
			w := f.Weight
			if w == 0 {
				w = 1
			}
			out = []res{{f.Aspect, 1 / w}}
		} else {
			low := m & -m
			rest := m ^ low
			for sub := rest; ; sub = (sub - 1) & rest {
				if a := low | sub; a != m {
					for _, l := range all(a) {
						for _, r := range all(m ^ a) {
							for _, c := range []Cut{H, V} {
								f := FracL(c, l.a, r.a)
								out = append(out, res{Aspect(c, l.a, r.a), math.Min(l.s*f, r.s*(1-f))})
							}
						}
					}
				}
				if sub == 0 {
					break
				}
			}
		}
		memo[m] = out
		return out
	}
	best := 0.0
	for _, r := range all(uint32(1)<<len(feeds) - 1) {
		best = math.Max(best, r.s*math.Min(r.a/screen, screen/r.a))
	}
	return best
}

func TestNearOptimal(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	shapes := []float64{16.0 / 9, 4.0 / 3, 1, 9.0 / 16, 21.0 / 9}
	for trial := 0; trial < 40; trial++ {
		n := 1 + r.Intn(6)
		feeds := make([]Feed, n)
		for i := range feeds {
			feeds[i] = Feed{Aspect: shapes[r.Intn(len(shapes))], Weight: float64(1 + r.Intn(2))}
		}
		screen := []float64{16.0 / 9, 1, 9.0 / 16, 21.0 / 9}[r.Intn(4)]
		s, err := Solve(feeds)
		if err != nil {
			t.Fatal(err)
		}
		got, want := s.Fit(screen).Smallest, exhaustive(feeds, screen)
		if got < 0.97*want || got > want+1e-9 {
			t.Errorf("trial %d: %d feeds on %.2f: got %.4f, exact best %.4f", trial, n, screen, got, want)
		}
	}
}

func checkGeometry(t *testing.T, feeds []Feed, screen float64, w Wall) {
	t.Helper()
	if len(w.Tiles) != len(feeds) {
		t.Fatalf("got %d tiles for %d feeds", len(w.Tiles), len(feeds))
	}
	area := 0.0
	for i, a := range w.Tiles {
		if a.Feed != i {
			t.Fatalf("tile %d holds feed %d", i, a.Feed)
		}
		if a.X < -1e-9 || a.Y < -1e-9 || a.X+a.W > 1+1e-9 || a.Y+a.H > 1+1e-9 {
			t.Errorf("tile %d leaves the screen: %+v", i, a)
		}
		if got := a.W * screen / a.H; math.Abs(got-feeds[i].Aspect) > 1e-6 {
			t.Errorf("tile %d has aspect %.4f, feed is %.4f", i, got, feeds[i].Aspect)
		}
		area += a.W * a.H
		for _, b := range w.Tiles[i+1:] {
			ox := math.Min(a.X+a.W, b.X+b.W) - math.Max(a.X, b.X)
			oy := math.Min(a.Y+a.H, b.Y+b.H) - math.Max(a.Y, b.Y)
			if ox > 1e-9 && oy > 1e-9 {
				t.Errorf("tiles %d and %d overlap", i, b.Feed)
			}
		}
	}
	if math.Abs(area-w.Fill) > 1e-6 {
		t.Errorf("tiles cover %.6f of the screen, wall fills %.6f: not gap-free", area, w.Fill)
	}
}

func TestGeometry(t *testing.T) {
	for _, tc := range []struct {
		feeds  []Feed
		screen float64
	}{
		{[]Feed{{16.0 / 9, 1}}, 16.0 / 9},
		{[]Feed{{16.0 / 9, 1}, {16.0 / 9, 1}, {16.0 / 9, 1}, {4.0 / 3, 1}, {1, 1}}, 16.0 / 9},
		{[]Feed{{16.0 / 9, 2}, {16.0 / 9, 1}, {16.0 / 9, 1}, {9.0 / 16, 1}}, 9.0 / 16},
	} {
		s, err := Solve(tc.feeds)
		if err != nil {
			t.Fatal(err)
		}
		checkGeometry(t, tc.feeds, tc.screen, s.Fit(tc.screen))
	}
}

func TestFourCamerasMakeAGrid(t *testing.T) {
	cams := []Feed{{16.0 / 9, 1}, {16.0 / 9, 1}, {16.0 / 9, 1}, {16.0 / 9, 1}}
	s, _ := Solve(cams)
	w := s.Fit(16.0 / 9)
	if math.Abs(w.Fill-1) > 1e-9 || math.Abs(w.Smallest-0.25) > 1e-9 {
		t.Errorf("4 cameras on 16:9: want a full 2×2 grid, got fill %.3f smallest %.3f", w.Fill, w.Smallest)
	}
}

func TestWeightMakesAFeedBigger(t *testing.T) {
	// A grid gives four equal tiles, so a modest weight changes nothing: the
	// smallest feed per unit of weight is already best. Weight 6 must win.
	cams := []Feed{{16.0 / 9, 6}, {16.0 / 9, 1}, {16.0 / 9, 1}, {16.0 / 9, 1}}
	s, _ := Solve(cams)
	w := s.Fit(16.0 / 9)
	big := w.Tiles[0].W * w.Tiles[0].H
	for _, tl := range w.Tiles[1:] {
		if a := tl.W * tl.H; big < 2*a {
			t.Errorf("weighted feed has area %.3f, another has %.3f", big, a)
		}
	}
}

func TestManyCamerasAreFast(t *testing.T) {
	var feeds []Feed
	for range 19 {
		feeds = append(feeds, Feed{16.0 / 9, 1})
	}
	for range 4 {
		feeds = append(feeds, Feed{4.0 / 3, 1})
	}
	feeds = append(feeds, Feed{1, 1}, Feed{3, 1})
	start := time.Now()
	s, err := Solve(feeds)
	if err != nil {
		t.Fatal(err)
	}
	w := s.Fit(16.0 / 9)
	checkGeometry(t, feeds, 16.0/9, w)
	if d := time.Since(start); d > 10*time.Second && !raceOn {
		t.Errorf("25 feeds took %v", d)
	}
	t.Logf("25 feeds: %d states, %.3g shapes, smallest %.4f, fill %.3f, %v",
		s.Stats.States, s.Count(), w.Smallest, w.Fill, time.Since(start))
}

func TestNearlyEqualShapesShareAKind(t *testing.T) {
	feeds := []Feed{{960.0 / 538, 1}, {16.0 / 9, 1}, {1280.0 / 722, 1}, {4.0 / 3, 1}}
	kinds, kindOf, err := Group(feeds)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kindOf[0] != kindOf[1] || kindOf[1] != kindOf[2] {
		t.Errorf("want two kinds (16:9-ish, 4:3), got %d: %v", len(kinds), kindOf)
	}
	s, _ := Solve(feeds)
	checkGeometry(t, feeds, 16.0/9, s.Fit(16.0/9)) // tiles keep exact shapes
}

func TestExtremeShapes(t *testing.T) {
	// a 12:1 panoramic camera and a 1:10 strip used to fall outside the range
	feeds := []Feed{{12, 1}, {16.0 / 9, 1}, {16.0 / 9, 1}, {0.1, 1}}
	s, err := Solve(feeds)
	if err != nil {
		t.Fatal(err)
	}
	for _, screen := range []float64{16.0 / 9, 32.0 / 9} {
		checkGeometry(t, feeds, screen, s.Fit(screen))
	}
}

// Many feeds in different sizes are bounded by MaxWork, and the solver
// keeps no search tables once solved.
func TestTooManySizesAreEvened(t *testing.T) {
	var feeds []Feed
	for i := range 25 {
		w := 1.0
		if i < 9 {
			w = 1.5 + float64(i)*0.5 // ten different sizes
		}
		feeds = append(feeds, Feed{16.0 / 9, w})
	}
	start := time.Now()
	if _, err := SolveUpTo(feeds, ExhaustiveUpTo); err != ErrTooManySizes {
		t.Fatalf("want ErrTooManySizes, got %v", err)
	}
	sv, err := Solve(feeds)
	if err != nil || !sv.Evened {
		t.Fatalf("Solve should even out the sizes: %v", err)
	}
	if time.Since(start) > 3*time.Second && !raceOn {
		t.Error("too slow")
	}
	// a typical enlarged wall still solves
	feeds = feeds[:16]
	for i := range feeds {
		feeds[i].Weight = 1
	}
	feeds[0].Weight, feeds[1].Weight = 4, 2
	if _, err := Solve(feeds); err != nil {
		t.Fatal(err)
	}
}

func TestFitOddScreens(t *testing.T) {
	s, err := Solve([]Feed{{16.0 / 9, 1}, {4.0 / 3, 1}, {1, 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, screen := range []float64{math.NaN(), 0, -1, math.Inf(1)} {
		if w := s.Fit(screen); len(w.Tiles) != 3 {
			t.Errorf("Fit(%v): %d tiles", screen, len(w.Tiles))
		}
	}
}
