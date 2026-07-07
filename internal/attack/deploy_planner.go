package attack

import (
	"image"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
)

// RetryPolicy defines retry behavior for a unit deployment.
type RetryPolicy struct {
	MaxRetries   int
	RetryDelay   time.Duration
	VerifyAfter  bool
}

// UnitPlan represents a resolved deployment plan for a single unit.
type UnitPlan struct {
	Unit       strategy.Unit
	Slot       *TrackedSlot
	IsSpell    bool
	IsHero     bool
	IsSiege    bool
	IsAbility  bool
	Priority   int // 0=spell, 1=regular, 2=ability
	Retry      RetryPolicy
}

// PhasePlan represents a resolved deployment plan for a phase.
type PhasePlan struct {
	Phase     strategy.Phase
	UnitPlans []UnitPlan
	Edge      string
}

// DeployPlanner resolves strategy YAML into concrete deployment plans.
type DeployPlanner struct {
	slotManager *SlotManager
	pCfg        PrecisionConfig
	targetEdge  string
	w, h        int
	defaultRetry RetryPolicy
	logger      zerolog.Logger
}

// NewDeployPlanner creates a new deployment planner.
func NewDeployPlanner(
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	targetEdge string,
	w, h int,
	logger zerolog.Logger,
) *DeployPlanner {
	return &DeployPlanner{
		slotManager: slotManager,
		pCfg:        pCfg,
		targetEdge:  targetEdge,
		w:           w,
		h:           h,
		defaultRetry: RetryPolicy{
			MaxRetries:  3,
			RetryDelay:  500 * time.Millisecond,
			VerifyAfter: true,
		},
		logger: logger.With().Str("component", "deploy_planner").Logger(),
	}
}

// PlanDeployment resolves all phases into concrete deployment plans.
func (dp *DeployPlanner) PlanDeployment(s *strategy.DynamicStrategy) []PhasePlan {
	var plans []PhasePlan

	for _, phase := range s.Phases {
		plan := dp.planPhase(phase)
		plans = append(plans, plan)
	}

	return plans
}

// planPhase resolves a single phase into a PhasePlan.
func (dp *DeployPlanner) planPhase(phase strategy.Phase) PhasePlan {
	plan := PhasePlan{
		Phase: phase,
		Edge:  dp.targetEdge,
	}

	// Sort units: Spells first, then regular, then abilities
	var spells []strategy.Unit
	var regular []strategy.Unit
	var abilities []strategy.Unit

	for _, unit := range phase.Units {
		isAbility := unit.Pattern == "Ability" || phase.Pattern == "Ability"
		unitName := strings.ToLower(strings.TrimSpace(unit.Name))

		if isAbility {
			abilities = append(abilities, unit)
		} else if isSpellStatic(unitName) {
			spells = append(spells, unit)
		} else {
			regular = append(regular, unit)
		}
	}

	// Build unit plans in order: spells → regular → abilities
	allUnits := append(spells, regular...)
	allUnits = append(allUnits, abilities...)

	for _, unit := range allUnits {
		unitPlan := dp.planUnit(unit, phase)
		if unitPlan.Slot != nil {
			plan.UnitPlans = append(plan.UnitPlans, unitPlan)
		}
	}

	return plan
}

// planUnit resolves a single unit into a UnitPlan.
func (dp *DeployPlanner) planUnit(unit strategy.Unit, phase strategy.Phase) UnitPlan {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isAbility := unit.Pattern == "Ability" || phase.Pattern == "Ability"

	plan := UnitPlan{
		Unit:      unit,
		IsSpell:   isSpellStatic(unitName),
		IsHero:    isHeroStatic(unitName),
		IsSiege:   isSiegeStatic(unitName),
		IsAbility: isAbility,
		Retry:     dp.defaultRetry,
	}

	// Set priority
	if plan.IsSpell {
		plan.Priority = 0
	} else if plan.IsAbility {
		plan.Priority = 2
	} else {
		plan.Priority = 1
	}

	// Find slot for this unit
	slot := dp.slotManager.GetSlot(unitName)
	if slot == nil {
		dp.logger.Warn().Str("unit", unit.Name).Msg("unit not found in bar")
		return plan
	}

	plan.Slot = slot
	return plan
}

// GetStrategyUnitNames returns all unit names from a strategy.
func GetStrategyUnitNames(s *strategy.DynamicStrategy) []string {
	var names []string
	seen := make(map[string]bool)

	for _, phase := range s.Phases {
		for _, unit := range phase.Units {
			name := strings.ToLower(strings.TrimSpace(unit.Name))
			if !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	return names
}

// ResolveHeroTargets returns hero units from a phase plan.
func ResolveHeroTargets(plan PhasePlan) []UnitPlan {
	var heroes []UnitPlan
	for _, up := range plan.UnitPlans {
		if up.IsHero && !up.IsAbility {
			heroes = append(heroes, up)
		}
	}
	return heroes
}

// ResolveAbilityTargets returns ability units from a phase plan.
func ResolveAbilityTargets(plan PhasePlan) []UnitPlan {
	var abilities []UnitPlan
	for _, up := range plan.UnitPlans {
		if up.IsAbility {
			abilities = append(abilities, up)
		}
	}
	return abilities
}

// ResolveSpellTargets returns spell units from a phase plan.
func ResolveSpellTargets(plan PhasePlan) []UnitPlan {
	var spells []UnitPlan
	for _, up := range plan.UnitPlans {
		if up.IsSpell {
			spells = append(spells, up)
		}
	}
	return spells
}

// ResolveTroopTargets returns non-hero, non-spell, non-ability, non-siege
// units from a phase plan.
//
// Siege machines (Stone Slammer, Battle Blimp, etc.) are intentionally
// EXCLUDED here. They have their own DeploySiege path with the precise
// touch sequence CoC expects, and including them caused the historical
// "siege deployed twice" bug — once as a regular troop and once as a
// siege machine — which double-spends the unit and confuses the verifier.
func ResolveTroopTargets(plan PhasePlan) []UnitPlan {
	var troops []UnitPlan
	for _, up := range plan.UnitPlans {
		if !up.IsHero && !up.IsSpell && !up.IsAbility && !up.IsSiege {
			troops = append(troops, up)
		}
	}
	return troops
}

// ResolveSiegeTargets returns siege units from a phase plan.
func ResolveSiegeTargets(plan PhasePlan) []UnitPlan {
	var sieges []UnitPlan
	for _, up := range plan.UnitPlans {
		if up.IsSiege {
			sieges = append(sieges, up)
		}
	}
	return sieges
}

// ScaleEdgeForPhase scales an edge to current screen dimensions.
func ScaleEdgeForPhase(edge ManualEdge, pCfg PrecisionConfig, w, h int) ManualEdge {
	return ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
}

// GetDeploymentEdge returns the edge for a given unit.
func GetDeploymentEdge(unit UnitPlan, targetEdge string, pCfg PrecisionConfig, w, h int) (image.Point, image.Point) {
	// Heroes: outer edge point
	if unit.IsHero && !strings.Contains(strings.ToLower(unit.Unit.Name), "duke") {
		edge, ok := pCfg.Edges[targetEdge]
		if ok {
			scaled := ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
			return scaled.P1, scaled.P1
		}
	}

	// Dragon Duke: adjacent edge
	if strings.Contains(strings.ToLower(unit.Unit.Name), "duke") {
		adjacentEdges := map[string][]string{
			"TopLeft":     {"TopRight", "BottomLeft"},
			"TopRight":    {"TopLeft", "BottomRight"},
			"BottomLeft":  {"TopLeft", "BottomRight"},
			"BottomRight": {"TopRight", "BottomLeft"},
		}
		deploymentEdge := targetEdge
		if adj, ok := adjacentEdges[targetEdge]; ok && len(adj) > 0 {
			deploymentEdge = adj[0] // Use first adjacent edge
		}
		edge, ok := pCfg.Edges[deploymentEdge]
		if ok {
			scaled := ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
			return scaled.P1, scaled.P2
		}
	}

	// Default: target edge line
	edge, ok := pCfg.Edges[targetEdge]
	if ok {
		scaled := ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
		return scaled.P1, scaled.P2
	}

	// Fallback: center
	center := image.Pt(w/2, h/2)
	return center, center
}
