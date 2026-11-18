// Package bot — village.go
//
// The bot can size its search floors from the account's own Town Hall instead of
// a static pair of numbers, using the export the player copies out of the game
// (internal/village). Nothing here touches the device: an account with no export
// imported simply keeps its configured floors.
package bot

import (
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/village"
	"github.com/rs/zerolog"
)

// resolveVillageFacts is what the bot knows about the account before it starts:
// the Town Hall the search floors are sized from, and whether the Builder Base is
// available. The Town Hall is search.town_hall_level when set, else the imported
// export; 0 means unknown and leaves the configured floors alone. The Builder
// Base is unlocked unless an imported export says it is not — enabling
// builder_base is explicit consent, and only the export can veto it.
func resolveVillageFacts(cfg *config.BotConfig, logger zerolog.Logger) (townHall int, builderBase bool) {
	builderBase = true
	if th := cfg.Search.TownHallLevel; th > 0 {
		logger.Info().
			Int("town_hall", th).
			Str("source", "search.town_hall_level").
			Msg("search floors are sized from the configured Town Hall")
		if v, err := village.Load(); err == nil {
			builderBase = v.BuilderBase
		}
		return th, builderBase
	}

	v, err := village.Load()
	if err != nil {
		logger.Info().
			Str("path", village.Path()).
			Msg("no village export imported; search floors stay as configured (import one with: bot_cli -import-village <export.json>)")
		return 0, builderBase
	}

	logger.Info().
		Int("town_hall", v.TownHall).
		Int("builders", v.Builders).
		Bool("builder_base", v.BuilderBase).
		Bool("lab", v.Lab).
		Int("pets", v.Pets).
		Str("read_at", v.ReadAt.Format(time.RFC3339)).
		Dur("age", v.Age(time.Now()).Round(time.Hour)).
		Msg("village export loaded; search floors follow its Town Hall")
	return v.TownHall, v.BuilderBase
}

// suggestedFloors reports the floors the account's Town Hall calls for. ok is
// false when the level is unknown or the feature is off, which leaves the
// configured values in charge.
func (b *Bot) suggestedFloors() (gold, elixir, dark int, ok bool) {
	if !b.cfg().Search.UseSuggestedFloors || b.villageTH <= 0 {
		return 0, 0, 0, false
	}
	g, e, d := village.SuggestedFloors(b.villageTH)
	if g <= 0 {
		return 0, 0, 0, false
	}
	return g, e, d, true
}

// clampFloors caps a floor above what a base can offer, saying so once. The
// symptom of an unreachable floor is an endless search, which looks identical to
// a broken search.
func (b *Bot) clampFloors(gold, elixir, dark int) (int, int, int) {
	g, e, d, capped := village.ClampToReachable(gold, elixir, dark)
	if len(capped) == 0 {
		return g, e, d
	}
	if !b.floorClampWarned.Swap(true) {
		b.logger.Warn().
			Strs("capped", capped).
			Int("gold", g).
			Int("elixir", e).
			Int("dark_elixir", d).
			Msg("search floor is above what any base offers (an extra zero?); capped, or the search would skip every base forever")
	}
	return g, e, d
}
