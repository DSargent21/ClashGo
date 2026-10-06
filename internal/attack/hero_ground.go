package attack

import (
	"image"
	"math"
	"sort"

	"github.com/Ducky705/ClashGO/pkg/formula"
)

// Hero ground selection — where a hero (or a sweep hero retry) is allowed to
// land.
//
// Live defect this file exists for (run10, 2026-09-27, 1280x720, BottomRight):
// the bot dropped both heroes at (935,485), the midpoint of the pinned
// reference-geometry edge line, and the game refused both — the slot stayed on
// its resting ratio (BK 0.7186→0.6419, Duke 0.3871→0.7034) and the ability pass
// then correctly refused to fire on an unconfirmed hero. Seven seconds later
// the sweep retried the SAME point and both heroes deployed (delta 0.539 and
// 0.613). The difference was not the tap, it was the ground: the troops and
// spells had all deployed along the formula line at x≈1088 in that battle,
// while the pinned edge line's midpoint sits ~150px further inland — inside the
// red no-deploy line at the moment the hero phase ran.
//
// The pinned line bypasses DeployLineCalculator entirely (the orchestrator
// swaps it in for every consumer once the user pins a corner), so nothing
// downstream ever asked "is this point actually deployable in THIS battle".
// These helpers ask exactly that, using the battle's own red-line outline.
//
// A zero-value RedZone (no usable outline) keeps the legacy behaviour: the
// band-filtered point closest to the band centroid. "Unknown" must not move a
// point the user pinned.

// heroDroppedDelta is the slot-activity transition that proves a hero left the
// bar: all hero icons sit at 0.6-0.75 and all cooldown silhouettes at ~0.10, so
// the delta is consistent across heroes even though the absolute values are not
// (see slot_settle.go for the live numbers).
const heroDroppedDelta = 0.15

// heroConsumedRatio is the slot-activity ceiling of a hero card that has left
// the bar.
//
// Live 720p frames (2026-09-27, both a TopLeft and a BottomRight battle) measure
// every resting or selected hero icon at 0.387-0.719 and every deployed hero's
// card at 0.094-0.132, so the two states separate cleanly at 0.25 — while the
// delta test alone does not. Tapping a hero's card moves that hero's own slot by
// whatever its highlight happens to do: Barbarian King 0.7186 -> 0.6393 (+0.08)
// and Dragon Duke 0.3871 -> 0.7047 (-0.32). A card that is merely SELECTED — the
// state a refused drop leaves behind — therefore looks like a failure with
// delta >= 0.15 and like a success with delta <= 0, and either way the ability
// pass then refuses to fire, the sweep spends the hero's second and last card
// tap re-placing it, and the hero finishes the battle with no ability. Reading
// the absolute post-drop value removes that whole class of misjudgement.
const heroConsumedRatio = 0.25

// heroPlaced reports whether the settled post-drop read proves the hero left the
// bar: either the classic resting->cooldown delta, or the absolute consumed-card
// ceiling. Only the post value is required, because the pre value may come from
// the stale battle-start fallback frame when a settle capture fails (live: the
// Duke's fallback read there is 0.387 against a real 0.705, which turns a working
// drop into a negative delta).
func heroPlaced(pre, post float64, havePost bool) bool {
	if !havePost {
		return false
	}
	if post <= heroConsumedRatio {
		return true
	}
	return pre-post >= heroDroppedDelta
}

// maxHeroGroundCandidates bounds how many alternate drop points the field-tap
// retry may work through. Each retry is a FIELD tap only (the card stays
// selected after a refused drop), so the hero's 2-card-tap ceiling is
// untouched; the bound exists to keep a hero that cannot be placed at all from
// spending the deploy phase on taps.
const maxHeroGroundCandidates = 4

// heroFieldRetries is how many alternate-ground FIELD taps an unconfirmed drop
// may add. These are not card taps: the hero card budget (place + ability)
// is never touched by them.
//
// Three, not two, because escaping a no-deploy pocket takes a real walk — see
// heroOutwardTiles: live 2026-09-27 the pocket at the King's y was still there
// 38 px out, and the ground only became deployable ~100 px further out (or ~12 s
// later, once the pocket had receded).
const heroFieldRetries = 3

// heroOutwardTiles is how far, in village tiles, an unconfirmed drop walks
// toward the nearest screen edge on each retry. The steps are deliberately
// spaced out rather than consecutive: the point of the walk is to leave the
// no-deploy region in as few taps as possible, so the first retry already steps
// well clear of a one-tile pocket and the last one is near the screen margin.
var heroOutwardTiles = []int{2, 4, 6}

// fieldEdgePad keeps a clamped ladder step clear of the outermost pixels.
const fieldEdgePad = 8

// tileStepPx approximates one village tile at the live geometry (the same
// ~19.5 reference px the formula's radial push uses, scaled by the frame
// height). Used to walk a blocked point out of the red line in whole tiles.
func tileStepPx(h int) int {
	step := int(19.5 * float64(h) / 732.0)
	if step < 12 {
		step = 12
	}
	return step
}

// bandPoints keeps the points that sit inside the deployable vertical band:
// below the top resource HUD (yTopMin) and above the troop bar (85% of the
// frame). A pinned line bypasses DeployLineCalculator's own band check, so this
// is the only thing keeping a pinned hero drop off a HUD.
func bandPoints(points []image.Point, h int) []image.Point {
	bottom := int(float64(h) * 0.85)
	band := make([]image.Point, 0, len(points))
	for _, p := range points {
		if p.Y >= yTopMin && p.Y <= bottom {
			band = append(band, p)
		}
	}
	return band
}

// nearestToCentroid returns the point closest to the centroid of the set — the
// middle of the line, which is away from both screen edges and both HUDs.
func nearestToCentroid(points []image.Point) image.Point {
	if len(points) == 0 {
		return image.Point{}
	}
	cx, cy := 0, 0
	for _, p := range points {
		cx += p.X
		cy += p.Y
	}
	cx /= len(points)
	cy /= len(points)
	dist2 := func(p image.Point) int {
		dx, dy := p.X-cx, p.Y-cy
		return dx*dx + dy*dy
	}
	best := points[0]
	bestD := dist2(best)
	for _, p := range points[1:] {
		if d := dist2(p); d < bestD {
			best, bestD = p, d
		}
	}
	return best
}

// zoneClearance is the distance from pt to the nearest vertex of the red-line
// outline: how much clear ground a drop at pt has. The outline is a dense
// contour, so the nearest vertex approximates the distance to the line itself
// well enough to rank candidates. Only meaningful for points OUTSIDE the zone.
func zoneClearance(pt image.Point, poly []image.Point) float64 {
	if len(poly) == 0 {
		return 0
	}
	best := math.Inf(1)
	for _, v := range poly {
		dx := float64(pt.X - v.X)
		dy := float64(pt.Y - v.Y)
		if d := math.Hypot(dx, dy); d < best {
			best = d
		}
	}
	return best
}

// pushPointOutOfZone walks pt away from (cx,cy) one tile per step until it
// leaves the red line. It is the same radial rule the formula path uses, and it
// keeps the moved tap on the axis the caller aimed along instead of nudging it
// a few pixels inside the same zone.
func pushPointOutOfZone(pt image.Point, cx, cy, step int, zone RedZone) (image.Point, bool) {
	if step <= 0 || len(zone.Polygon) < 3 {
		return pt, false
	}
	dx := float64(pt.X - cx)
	dy := float64(pt.Y - cy)
	length := math.Hypot(dx, dy)
	if length < 1 {
		return pt, false
	}
	ux, uy := dx/length, dy/length
	cur := pt
	for i := 0; i < 24; i++ {
		cur = image.Pt(cur.X+int(math.Round(ux*float64(step))), cur.Y+int(math.Round(uy*float64(step))))
		if !zone.Inside(cur.X, cur.Y) {
			return cur, true
		}
	}
	return pt, false
}

// heroFormulaCandidates returns the drop points the strategy's formula pins for
// a hero: the point itself, or samples spread along a pinned line.
//
// These are the strongest ground evidence available at hero time. The formula is
// the SAME ground the troop and spell phases deploy on, and the orchestrator has
// already clamped it into the deploy band and pushed any point that sat inside
// the red line outward — so by the time the hero phase runs, taps on this ground
// have just been accepted by the game in this very battle. Live run12
// (2026-09-27, TopRight) is what settled it: the heroes were aimed at the pinned
// reference-geometry edge instead (a diagonal cutting INTO the base), the game
// refused every one of them at (989,147), and the balloon/ED pass that had
// deployed seconds earlier on the formula line at x=1088 was untouched.
//
// The alternates for a point pin are the neighbouring tiles along the same axis,
// because a single tile can be blocked by the building standing on it.
func heroFormulaCandidates(entry formula.UnitEntry, h int) []image.Point {
	switch {
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		p1, p2 := entry.P1.Image(), entry.P2.Image()
		out := make([]image.Point, 0, 3)
		for _, t := range []float64{0.25, 0.5, 0.75} {
			out = append(out, image.Pt(
				p1.X+int(float64(p2.X-p1.X)*t),
				p1.Y+int(float64(p2.Y-p1.Y)*t),
			))
		}
		return bandPoints(out, h)
	case entry.IsPoint() && entry.P != nil:
		p := entry.P.Image()
		step := tileStepPx(h)
		out := []image.Point{p,
			image.Pt(p.X, p.Y-step),
			image.Pt(p.X, p.Y+step),
			image.Pt(p.X, p.Y-2*step),
		}
		return bandPoints(out, h)
	}
	return nil
}

// groundAxis reports the shared coordinate of ground a single deploy pass just
// used: a vertical line (every point shares x) or a horizontal one (every point
// shares y). The 30px tolerance is one village tile, so a line drawn by hand is
// still recognised as an axis while two different lines are not merged.
func groundAxis(points []image.Point) (axis int, vertical, ok bool) {
	if len(points) == 0 {
		return 0, false, false
	}
	x, y := points[0].X, points[0].Y
	sameX, sameY := true, true
	for _, p := range points[1:] {
		if absInt(p.X-x) > 30 {
			sameX = false
		}
		if absInt(p.Y-y) > 30 {
			sameY = false
		}
	}
	switch {
	case sameX:
		return x, true, true
	case sameY:
		return y, false, true
	}
	return 0, false, false
}

// axisCandidates returns pt snapped onto the troop pass's ground axis, followed
// by the neighbours a refused drop should actually retry.
//
// Only the perpendicular coordinate comes from the caller's intent: the push
// that moved it is not trusted (live run13 the orchestrator pushed two hero pins
// sideways onto ground the game refused, x=45 and x=118, against a troop line
// the game had just accepted at x=192).
//
// The RETRY order is outward-first. A drop is refused when the point sits inside
// the game's no-deploy area — the game says so on screen ("You cannot deploy
// troops on the Red area!", captured mid-hero-phase in the 2026-09-27 TopLeft
// run) — and that area is a REGION, so the neighbouring tiles along the same line
// are refused with it. Live proof: both heroes were refused at x=192, y=504/216,
// and the sweep's later tap at the SAME point was accepted only because the area
// had shrunk by then. Stepping the retry a tile or two toward the nearest screen
// edge leaves the region immediately, which is what turns a wasted hero phase
// into a placement on the first field tap. The along-axis tiles are kept after
// the outward ones, for the other failure mode (one building blocks one tile).
func axisCandidates(intent image.Point, axis int, vertical bool, h, w int) []image.Point {
	step := tileStepPx(h)
	var out []image.Point
	if vertical {
		// Vertical troop line: the deployable strip runs along the right or the
		// left screen edge, so "outward" is whichever way the axis is closer to.
		sign := 1
		if axis < w/2 {
			sign = -1
		}
		out = append(out, image.Pt(axis, intent.Y))
		seen := map[int]bool{axis: true}
		for _, tiles := range heroOutwardTiles {
			x := clampInt(axis+sign*tiles*step, xMinPad, w-xMinPad)
			if seen[x] {
				continue
			}
			seen[x] = true
			out = append(out, image.Pt(x, intent.Y))
		}
		out = append(out, image.Pt(axis, intent.Y-step), image.Pt(axis, intent.Y+step))
	} else {
		// Horizontal troop line: outward is up for a top side, down for a bottom.
		sign, lo, hi := 1, yTopMin, int(float64(h)*0.85)
		if axis < h/2 {
			sign = -1
		}
		out = append(out, image.Pt(intent.X, axis))
		seen := map[int]bool{axis: true}
		for _, tiles := range heroOutwardTiles {
			y := clampInt(axis+sign*tiles*step, lo, hi)
			if seen[y] {
				continue
			}
			seen[y] = true
			out = append(out, image.Pt(intent.X, y))
		}
		out = append(out, image.Pt(intent.X-step, axis), image.Pt(intent.X+step, axis))
	}
	return bandPoints(out, h)
}

// orderOutwardFirst re-orders hero ground candidates from the outside of the
// field inward: the farther a point sits from the screen centre, the farther it
// is from the base's no-deploy pocket, which hugs the middle of the map. It is
// the first-tap ordering for battles whose red line is UNKNOWN (see the call in
// HeroManager.heroDropCandidates); with a real outline the clearance order wins.
func orderOutwardFirst(cands []image.Point, w, h int) {
	mid := image.Pt(w/2, h/2)
	sort.SliceStable(cands, func(i, j int) bool {
		return dist2(cands[i], mid) > dist2(cands[j], mid)
	})
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// absInt is a small absolute-value helper for pixel deltas.
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// heroGroundCandidates returns the hero drop points to try, best first.
//
//   - No usable red outline: one candidate, the legacy centroid-near point.
//   - Usable outline: the band points OUTSIDE the line, ordered by clearance
//     (most clear ground first). A blocked line — every in-band point inside
//     the line, which is what a stale pinned edge looks like — gets a radial
//     push of its best point appended first, with the in-line points kept last
//     as fallbacks.
//
// Callers use [0] as the drop point and the rest as the field-tap retries.
func heroGroundCandidates(points []image.Point, h int, zone RedZone) []image.Point {
	band := bandPoints(points, h)
	if len(band) == 0 {
		return nil
	}
	if len(zone.Polygon) < 3 {
		return []image.Point{nearestToCentroid(band)}
	}

	var outside, inside []image.Point
	for _, p := range band {
		if zone.Inside(p.X, p.Y) {
			inside = append(inside, p)
		} else {
			outside = append(outside, p)
		}
	}
	byClearance := func(ps []image.Point) {
		sort.SliceStable(ps, func(i, j int) bool {
			return zoneClearance(ps[i], zone.Polygon) > zoneClearance(ps[j], zone.Polygon)
		})
	}
	byClearance(outside)
	byClearance(inside)

	out := make([]image.Point, 0, len(band)+1)
	if len(outside) > 0 {
		out = append(out, outside...)
	} else if cx, cy := zone.Centre(); cx != 0 || cy != 0 {
		// Every candidate is inside the line. Push the best of them out so
		// the first tap still has a chance, then keep the blocked points as
		// last-resort fallbacks.
		if pushed, ok := pushPointOutOfZone(nearestToCentroid(band), cx, cy, tileStepPx(h), zone); ok {
			out = append(out, pushed)
		}
	}
	out = append(out, inside...)
	return out
}

// outwardHeroLadder returns the ground to try after a hero drop is refused.
//
// A refusal is a region, not a pixel. Live 2026-09-27, all four sides: the user
// pinned a hero point that sits inside the base's no-deploy pocket, the game
// refused the tap (the card stayed armed and full), and the only other candidate
// a missing red zone produces was the line's MIDPOINT — which is just as far
// inside the pocket. All eight hero drops were refused, the sweep placed the
// heroes seconds later at ground ~40 px further OUT, and because the sweep runs
// after the ability pass, both abilities were lost.
//
// So the ladder walks outward: first along the user's own line (the ground this
// battle's troops just used, so the most likely to be accepted), toward the end
// farther from the middle of the field, then radially away from the field centre
// if the line runs out. Outward steps cost field taps only — never a card tap —
// so the hero keeps its second tap for its ability.
func outwardHeroLadder(pin image.Point, line []image.Point, w, h, n int) []image.Point {
	if n <= 0 {
		return nil
	}
	mid := image.Pt(w/2, h/2)
	out := make([]image.Point, 0, n)
	seen := map[image.Point]bool{pin: true}

	if len(line) >= 2 {
		idx := nearestLineIndex(line, pin)
		step := -2
		endIdx := 0
		if dist2(line[len(line)-1], mid) > dist2(line[0], mid) {
			step = 2 // the far end is the last point, so walk up the line
			endIdx = len(line) - 1
		}
		for i := idx + step; len(out) < n && i >= 0 && i < len(line); i += step {
			p := line[i]
			if seen[p] {
				continue
			}
			out = append(out, p)
			seen[p] = true
		}
		if len(out) < n && !seen[line[endIdx]] {
			out = append(out, line[endIdx])
			seen[line[endIdx]] = true
		}
	}

	dx, dy := pin.X-mid.X, pin.Y-mid.Y
	if dx == 0 && dy == 0 {
		return out
	}
	norm := math.Hypot(float64(dx), float64(dy))
	ux, uy := float64(dx)/norm, float64(dy)/norm
	base := tileStepPx(h)
	for i := 2; len(out) < n && i <= 2*(n+2); i++ {
		p := clampToField(image.Pt(
			pin.X+int(ux*float64(base*i)),
			pin.Y+int(uy*float64(base*i)),
		), w, h)
		if seen[p] {
			continue
		}
		if len(out) > 0 && p == out[len(out)-1] {
			break // already against the border
		}
		out = append(out, p)
		seen[p] = true
	}
	return out
}

// nearestLineIndex returns the index of the line point closest to pt.
func nearestLineIndex(line []image.Point, pt image.Point) int {
	best, bestD := 0, -1
	for i, p := range line {
		d := dist2(p, pt)
		if bestD < 0 || d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// dist2 is a squared distance, which is all the ordering questions here need.
func dist2(a, b image.Point) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	return dx*dx + dy*dy
}

// clampToField keeps a ladder step on the battlefield: inside the deploy band
// vertically, and clear of the screen edge horizontally (a tap on the outermost
// pixels is unreliable and can land on the device's own status bar).
func clampToField(p image.Point, w, h int) image.Point {
	p = ClampToDeployBand(p, UICutoff(h))
	if p.X < fieldEdgePad {
		p.X = fieldEdgePad
	}
	if p.X > w-fieldEdgePad {
		p.X = w - fieldEdgePad
	}
	return p
}

// runHeroFieldRetries re-aims the FIELD tap over cands[1:] after a drop the slot
// did not confirm, until one confirms (delta >= heroDroppedDelta) or the retry
// budget is spent. It fires NO card tap: a refused drop leaves the hero's card
// selected, so the field alone can be re-aimed and the hero's remaining — and
// last — card tap stays reserved for its ability. Every attempt is verified
// against the previous reading carried forward, so no extra pre-capture is
// needed. `fire` sends one field tap, `sample` sleeps-and-reads the settled
// slot ratio, `budgetGone` answers whether the deploy budget is spent, and
// `report` receives each attempt's numbers for logging.
func runHeroFieldRetries(
	cands []image.Point,
	baseline float64,
	baselineOK bool,
	fire func(image.Point),
	sample func() (float64, bool),
	budgetGone func() bool,
	report func(attempt int, pt image.Point, pre, post, delta float64, won bool),
) bool {
	prev := baseline
	for i := 1; i < len(cands) && i-1 < heroFieldRetries; i++ {
		if !baselineOK || budgetGone() {
			return false
		}
		fire(cands[i])
		post, ok := sample()
		if !ok {
			return false
		}
		delta := prev - post
		won := heroPlaced(prev, post, true)
		report(i, cands[i], prev, post, delta, won)
		if won {
			return true
		}
		prev, baselineOK = post, true
	}
	return false
}

// bestDeployLinePoint picks the deploy-line point most likely to be accepted by
// the game, ignoring the red zone (see heroGroundCandidates for the
// zone-aware picker every live deploy path uses). Retained for callers that
// only have the line — the verifier's hero retry and the geometry tests.
func bestDeployLinePoint(points []image.Point, h int) (image.Point, bool) {
	cands := heroGroundCandidates(points, h, RedZone{})
	if len(cands) == 0 {
		return image.Point{}, false
	}
	return cands[0], true
}
