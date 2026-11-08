package layout

import (
	"math"
	"math/rand"

	"github.com/vinodhalaharvi/dyna"
)

type LawResult = dyna.LawResult

// AllLaws checks Count, TopK and their Product on the wall's cut nodes.
func AllLaws(trials int, seed int64) []LawResult {
	r := rand.New(rand.NewSource(seed))
	kinds := []Feed{{16.0 / 9, 1}, {4.0 / 3, 2}}
	cuts := []Step{{Cut: H}, {Cut: V}}
	two := func(Step) int { return 2 }
	genCount := func(r *rand.Rand) float64 { return float64(r.Intn(6)) }
	eqCount := func(a, b float64) bool { return a == b }
	genTop := dyna.GenRanked(K, Step{Leaf: true})
	var out []LawResult
	out = append(out, dyna.CheckLaws(dyna.Count[Step](), dyna.LawSpec[Step, float64]{
		Name: "Count (+,×)", Labels: cuts, Arity: two, Gen: genCount, Eq: eqCount}, trials, r)...)
	out = append(out, dyna.CheckLaws(dyna.TopK(K, Score(kinds)), dyna.LawSpec[Step, dyna.Ranked[Step]]{
		Name: "TopK₁ (max,min)", Labels: cuts, Arity: two, Gen: genTop, Eq: dyna.RankedScoresEqual[Step]}, trials, r)...)
	out = append(out, dyna.CheckLaws(dyna.Product(dyna.Count[Step](), dyna.TopK(K, Score(kinds))), dyna.LawSpec[Step, Cell]{
		Name: "Count × TopK₁", Labels: cuts, Arity: two,
		Gen: func(r *rand.Rand) Cell { return Cell{A: genCount(r), B: genTop(r)} },
		Eq:  func(a, b Cell) bool { return eqCount(a.A, b.A) && dyna.RankedScoresEqual(a.B, b.B) }}, trials, r)...)
	return out
}

// DynaCheck compares the memoised dynamorphism with brute force.
type DynaCheck struct {
	Feeds        int  `json:"feeds"`
	Kinds        int  `json:"kinds"`
	Trees        int  `json:"trees"`
	States       int  `json:"states"`
	Alternatives int  `json:"alternatives"`
	OK           bool `json:"ok"`
}

// VerifyDyna checks that the memoised solve equals folding every wall tree
// on its own and ⊕-ing the results, for random feeds of a few kinds.
func VerifyDyna(n int, seed int64) DynaCheck {
	r := rand.New(rand.NewSource(seed))
	shapes := []float64{16.0 / 9, 4.0 / 3, 1, 9.0 / 16}
	feeds := make([]Feed, n)
	for i := range feeds {
		feeds[i] = Feed{Aspect: shapes[r.Intn(len(shapes))], Weight: float64(1 + r.Intn(2))}
	}
	kinds, kindOf, _ := Group(feeds)
	var goal State
	for _, k := range kindOf {
		goal[k]++
	}
	gr := NewGrades(Buckets, MaxAspect)
	alg := NewAlgebra(kinds, gr)
	dp, st := dyna.Dyna(alg, Splits(len(kinds)), goal)
	bf, trees := dyna.BruteForce(alg, Splits(len(kinds)), goal)
	ok := len(dp.NZ) == len(bf.NZ)
	for b := 0; ok && b < gr.B; b++ {
		x, y := dp.Get(b, Cell{}), bf.Get(b, Cell{})
		ok = math.Abs(x.A-y.A) < 1e-6 && dyna.RankedScoresEqual(x.B, y.B)
	}
	return DynaCheck{n, len(kinds), trees, st.States, st.Alternatives, ok}
}
