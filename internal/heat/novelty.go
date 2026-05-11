package heat

// NoveltyComponent converts a 0..1 novelty score to a T3 Component (max 5).
// Linear: Points = clamp(novelty, 0, 1) * 5. Raw carries the input novelty.
func NoveltyComponent(novelty float64) Component {
	n := novelty
	if n < 0 {
		n = 0
	} else if n > 1 {
		n = 1
	}
	return Component{Name: "novelty", Points: n * 5, Max: 5, Raw: novelty}
}
