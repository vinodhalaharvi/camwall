// Package layout finds the gap-free camera wall where the smallest feed is as
// large as possible on screen, as a dynamic program written with
// github.com/vinodhalaharvi/dyna.
//
//	F X       = Leaf kind | Split cut X X        one wall step
//	coalgebra   counts per kind → every way to split them in two
//	algebra     Lift(Count × TopK) graded by aspect ratio
//
// Feeds that share an aspect ratio and a weight are interchangeable, so the
// state is how many feeds of each kind remain, not which ones. A shop with 16
// identical cameras has 17 states instead of 65,536.
//
// Unlike a photo collage, a camera feed is never cropped: each tile keeps its
// feed's exact shape and the whole wall is letterboxed into the screen. The
// search maximises what a viewer sees, the on-screen area of the smallest
// feed, so the wall's own fit to the screen is part of the score.
package layout

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/vinodhalaharvi/dyna"
)

// Cut labels a binary split: H puts children side by side, V stacks them.
type Cut uint8

const (
	H Cut = iota
	V
)

func (c Cut) String() string {
	if c == H {
		return "H"
	}
	return "V"
}

// Aspect is the aspect monoid: side by side at equal height the widths add;
// stacked at equal width the heights (reciprocals) add.
func Aspect(c Cut, aL, aR float64) float64 {
	if c == H {
		return aL + aR
	}
	return 1 / (1/aL + 1/aR)
}

// FracL is the share of the parent's area that the left child receives.
func FracL(c Cut, aL, aR float64) float64 {
	if c == H {
		return aL / (aL + aR)
	}
	hL, hR := 1/aL, 1/aR
	return hL / (hL + hR)
}

// Feed is what the solver needs to know about a feed: its shape, and how
// large it should be relative to the others (2 means twice the area).
type Feed struct {
	Aspect float64
	Weight float64
}

const (
	MaxKinds = 16 // distinct (aspect, weight) pairs
	// MaxFeeds keeps a wall solvable in about a second; past this, each feed
	// is too small to watch anyway, so run a second wall.
	MaxFeeds = 25
)

// State counts the feeds of each kind still to be placed.
type State [MaxKinds]uint8

// Step labels one wall step: a leaf holding a feed of some kind, or a cut.
type Step struct {
	Leaf bool
	Kind uint8
	Cut  Cut
}

// ExhaustiveUpTo is the largest sub-wall split every possible way. Larger
// sub-walls only split off a block of one kind of feed (a row or grid of
// identical cameras) beside the rest, which keeps big mixed walls fast.
const ExhaustiveUpTo = 6

// Splits is the wall coalgebra. A single feed is a leaf; otherwise the counts
// are shared between two sides, cut side by side or stacked. Only one of each
// mirror pair (a, b) and (b, a) is kept.
func Splits(kinds int) dyna.Coalgebra[State, Step] {
	return SplitsUpTo(kinds, ExhaustiveUpTo)
}

// SplitsUpTo is Splits with a different exhaustive limit (for tests).
func SplitsUpTo(kinds, exhaustive int) dyna.Coalgebra[State, Step] {
	return func(s State) []dyna.Node[Step, State] {
		total, last := 0, 0
		for k := 0; k < kinds; k++ {
			total += int(s[k])
			if s[k] > 0 {
				last = k
			}
		}
		if total == 1 {
			return []dyna.Node[Step, State]{{Label: Step{Leaf: true, Kind: uint8(last)}}}
		}
		var out []dyna.Node[Step, State]
		var a State
		for {
			// odometer over 0 ≤ a ≤ s
			k := 0
			for k < kinds {
				if a[k] < s[k] {
					a[k]++
					break
				}
				a[k] = 0
				k++
			}
			if k == kinds {
				return out
			}
			var b State
			for i := 0; i < kinds; i++ {
				b[i] = s[i] - a[i]
			}
			if a == s || !lessEq(a, b, kinds) {
				continue
			}
			if total > exhaustive && !pure(a, kinds) && !pure(b, kinds) {
				continue
			}
			out = append(out,
				dyna.Node[Step, State]{Label: Step{Cut: H}, Kids: []State{a, b}},
				dyna.Node[Step, State]{Label: Step{Cut: V}, Kids: []State{a, b}})
		}
	}
}

// pure reports whether a side holds only one kind of feed.
func pure(a State, kinds int) bool {
	seen := false
	for i := 0; i < kinds; i++ {
		if a[i] > 0 {
			if seen {
				return false
			}
			seen = true
		}
	}
	return true
}

func lessEq(a, b State, kinds int) bool {
	for i := 0; i < kinds; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return true
}

// ---------- Grades: aspect-ratio buckets ----------

// Grades buckets log-aspect uniformly over [1/maxAspect, maxAspect]; a cut
// combines buckets through the aspect monoid.
type Grades struct {
	B        int
	lo, step float64
	center   []float64
	comb     [2][]int
}

func NewGrades(b int, maxAspect float64) *Grades {
	lo, hi := -math.Log(maxAspect), math.Log(maxAspect)
	g := &Grades{B: b, lo: lo, step: (hi - lo) / float64(b-1)}
	g.center = make([]float64, b)
	for i := range b {
		g.center[i] = math.Exp(lo + float64(i)*g.step)
	}
	for c := range 2 {
		g.comb[c] = make([]int, b*b)
		for i := range b {
			for j := range b {
				g.comb[c][i*b+j] = g.Of(Aspect(Cut(c), g.center[i], g.center[j]))
			}
		}
	}
	return g
}

// Of returns the bucket of an aspect, or -1 when it falls outside the range.
func (g *Grades) Of(a float64) int {
	x := math.Round((math.Log(a) - g.lo) / g.step)
	if x < 0 || x >= float64(g.B) {
		return -1
	}
	return int(x)
}

func (g *Grades) Center(b int) float64 { return g.center[b] }

func (g *Grades) grading(kinds []Feed) dyna.Grading[Step] {
	return dyna.Grading[Step]{N: g.B, Value: g.Center, Of: func(l Step, kids []int) int {
		if l.Leaf {
			return g.Of(kinds[l.Kind].Aspect)
		}
		return g.comb[l.Cut][kids[0]*g.B+kids[1]]
	}}
}

// ---------- The algebra: Count × TopK, lifted by aspect ----------

// K is how many walls are kept per subproblem and aspect.
const K = 1

const (
	Buckets   = 48
	MaxAspect = 8.0
)

// Score is the weighted area share of the smallest feed: a leaf of weight w
// starts at 1/w, and a cut scales each side by its share of the area and keeps
// the smaller. That is the (max, min) semiring; scaling is monotone, so TopK
// distributes over ⊕.
func Score(kinds []Feed) dyna.Score[Step] {
	return func(l Step, kids []float64, _ float64, grades []float64) float64 {
		if l.Leaf {
			return 1 / kinds[l.Kind].Weight
		}
		f := FracL(l.Cut, grades[0], grades[1])
		return math.Min(kids[0]*f, kids[1]*(1-f))
	}
}

type Cell = dyna.Pair[float64, dyna.Ranked[Step]]

func NewAlgebra(kinds []Feed, gr *Grades) dyna.Algebra[Step, dyna.Vec[Cell]] {
	return dyna.Lift(dyna.Product(dyna.Count[Step](), dyna.TopK(K, Score(kinds))), gr.grading(kinds))
}

// gradesFor covers the usual range of shapes (1:8 to 8:1), widened when a
// feed is itself extreme (a 12:1 panoramic camera), keeping the same
// resolution per bucket.
func gradesFor(kinds []Feed) *Grades {
	r := MaxAspect
	for _, k := range kinds {
		r = math.Max(r, 2*math.Max(k.Aspect, 1/k.Aspect))
	}
	b := Buckets
	if r > MaxAspect {
		b = int(math.Ceil(float64(Buckets) * math.Log(r) / math.Log(MaxAspect)))
	}
	return NewGrades(b, r)
}

// Solved is a solved wall: every shape it can take, indexed by aspect ratio,
// so fitting it to a screen of any shape is a lookup, not a new search.
type Solved struct {
	Feeds  []Feed
	kinds  []Feed
	kindOf []int // feed index → kind
	grades *Grades
	root   dyna.Vec[Cell]
	Stats  dyna.Stats
}

// SameShape is how close two aspect ratios must be (1%) for feeds to count
// as the same kind: a 960×538 camera and a 1920×1080 one are both 16:9.
// Tiles still keep each feed's exact shape; only the search groups them.
const SameShape = 0.01

// Group sorts feeds into kinds (nearly the same aspect, the same weight).
func Group(feeds []Feed) (kinds []Feed, kindOf []int, err error) {
	if len(feeds) == 0 {
		return nil, nil, errors.New("no feeds")
	}
	if len(feeds) > MaxFeeds {
		return nil, nil, fmt.Errorf("at most %d feeds", MaxFeeds)
	}
	kindOf = make([]int, len(feeds))
	for i, f := range feeds {
		if !(f.Aspect > 0) || math.IsInf(f.Aspect, 0) {
			return nil, nil, fmt.Errorf("feed %d: bad aspect ratio %v", i+1, f.Aspect)
		}
		if f.Weight == 0 {
			f.Weight = 1
		}
		if !(f.Weight > 0) {
			return nil, nil, fmt.Errorf("feed %d: weight must be positive", i+1)
		}
		k := -1
		for j, kd := range kinds {
			if math.Abs(math.Log(f.Aspect/kd.Aspect)) < SameShape && math.Abs(f.Weight-kd.Weight) < 1e-9 {
				k = j
				break
			}
		}
		if k < 0 {
			k = len(kinds)
			if k == MaxKinds {
				return nil, nil, fmt.Errorf("at most %d different feed shapes and weights", MaxKinds)
			}
			kinds = append(kinds, f)
		}
		kindOf[i] = k
	}
	return kinds, kindOf, nil
}

// Solve searches every gap-free guillotine wall of the feeds.
func Solve(feeds []Feed) (*Solved, error) {
	return SolveUpTo(feeds, ExhaustiveUpTo)
}

// SolveUpTo is Solve with a different exhaustive limit (for tests).
func SolveUpTo(feeds []Feed, exhaustive int) (*Solved, error) {
	kinds, kindOf, err := Group(feeds)
	if err != nil {
		return nil, err
	}
	var goal State
	for _, k := range kindOf {
		goal[k]++
	}
	gr := gradesFor(kinds)
	root, st := dyna.Dyna(NewAlgebra(kinds, gr), SplitsUpTo(len(kinds), exhaustive), goal)
	if len(root.NZ) == 0 {
		return nil, errors.New("no wall fits: the feeds' shapes are too extreme")
	}
	norm := make([]Feed, len(feeds))
	for i, f := range feeds {
		norm[i] = kinds[kindOf[i]]
		norm[i].Aspect = f.Aspect
	}
	return &Solved{Feeds: norm, kinds: kinds, kindOf: kindOf, grades: gr, root: root, Stats: st}, nil
}

// Count is how many distinct wall shapes were searched (mirror images and
// swaps of identical feeds counted once).
func (s *Solved) Count() float64 {
	total := 0.0
	for _, b := range s.root.NZ {
		total += s.root.At[b].A
	}
	return total
}

// ---------- Reading a wall out ----------

// Node is a wall tree evaluated with the feeds' exact aspect ratios.
type Node struct {
	Feed int     `json:"feed"` // feed index, -1 for a cut
	Cut  string  `json:"cut,omitempty"`
	A    float64 `json:"aspect"`
	L    *Node   `json:"l,omitempty"`
	R    *Node   `json:"r,omitempty"`
}

// Tile is where one feed goes, in fractions of the screen (0..1).
type Tile struct {
	Feed int     `json:"feed"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

// Wall is the best arrangement for one screen shape.
type Wall struct {
	Tiles  []Tile  `json:"tiles"`
	Aspect float64 `json:"aspect"` // the wall's own shape; letterboxed into the screen
	// Smallest is the on-screen area share of the smallest feed (per unit of
	// weight): what the search maximises.
	Smallest float64 `json:"smallest"`
	Fill     float64 `json:"fill"` // share of the screen the wall covers
	Tree     *Node   `json:"tree"`
}

// exact re-folds a witness with the real aspects, handing out actual feeds of
// each kind in reading order.
func (s *Solved) exact(e *dyna.Entry[Step], next []int) (*Node, float64) {
	if e.Label.Leaf {
		k := int(e.Label.Kind)
		f := -1
		for i := next[k]; i < len(s.kindOf); i++ {
			if s.kindOf[i] == k {
				f, next[k] = i, i+1
				break
			}
		}
		return &Node{Feed: f, A: s.Feeds[f].Aspect}, 1 / s.kinds[k].Weight
	}
	cut := e.Label.Cut
	l, sL := s.exact(e.Kids[0], next)
	r, sR := s.exact(e.Kids[1], next)
	frac := FracL(cut, l.A, r.A)
	return &Node{Feed: -1, Cut: cut.String(), A: Aspect(cut, l.A, r.A), L: l, R: r}, math.Min(sL*frac, sR*(1-frac))
}

// Fit returns the wall that shows the smallest feed largest on a screen of
// the given aspect ratio (width / height). The wall keeps every feed's shape
// exactly and is centred with bars where it does not fill the screen.
func (s *Solved) Fit(screen float64) Wall {
	var best Wall
	bestRank := -1.0
	for _, b := range s.root.NZ {
		for _, e := range s.root.At[b].B {
			n, share := s.exact(e, make([]int, len(s.kinds)))
			fill := math.Min(n.A/screen, screen/n.A)
			if rank := share * fill; rank > bestRank+1e-12 {
				bestRank = rank
				best = Wall{Aspect: n.A, Smallest: rank, Fill: fill, Tree: n}
			}
		}
	}
	// place the wall, centred
	w, h := 1.0, 1.0
	if best.Aspect > screen {
		h = screen / best.Aspect
	} else {
		w = best.Aspect / screen
	}
	best.Tiles = place(best.Tree, (1-w)/2, (1-h)/2, w, h, nil)
	sort.Slice(best.Tiles, func(i, j int) bool { return best.Tiles[i].Feed < best.Tiles[j].Feed })
	return best
}

func place(n *Node, x, y, w, h float64, out []Tile) []Tile {
	if n.Feed >= 0 {
		return append(out, Tile{Feed: n.Feed, X: x, Y: y, W: w, H: h})
	}
	if n.Cut == "H" {
		wl := w * FracL(H, n.L.A, n.R.A)
		out = place(n.L, x, y, wl, h, out)
		return place(n.R, x+wl, y, w-wl, h, out)
	}
	hl := h * FracL(V, n.L.A, n.R.A)
	out = place(n.L, x, y, w, hl, out)
	return place(n.R, x, y+hl, w, h-hl, out)
}
