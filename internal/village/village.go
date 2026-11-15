// Package village reads the game's own Data Export — Profile → Settings → More
// Settings → Data Export → Copy — into the few facts about the account that
// vision cannot answer.
//
// The export is the only source in the system that says what the account HAS
// rather than what is on screen right now: the Town Hall level, how many
// buildings exist, whether the Laboratory, the pet house or the Builder Base
// are built. Several farms in this space (COCFARM and coc-auto among them) use
// it for exactly that: to size loot floors and to stop trying to upgrade things
// the account does not own yet.
//
// Everything here is pure parsing of a file the player copies out of the game,
// so it is testable without a device. The bot reads the export if one has been
// imported; nothing navigates the game's settings menu.
package village

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
)

// Building ids the export uses in its "data" field. They are the game's own
// indices, not names, so they are stable across languages.
const (
	idTownHall     = 1000001
	idLaboratory   = 1000007
	idBuildersHut  = 1000015
	idBuilderHall  = 1000034
	idBobControl   = 1000065 // the builder base's B.O.B, who also works the home village
	idOttoOutpost  = 1000078 // the builder base's second builder
	fileNameExport = "village_export.json"
)

// Village is the export reduced to what the bot acts on.
type Village struct {
	TownHall int
	// Builders is the home village's builder count, huts plus B.O.B.
	Builders int
	// BuilderBase reports whether the second village is unlocked and in use.
	BuilderBase bool
	// BuilderBaseBuilders is the Master Builder, plus O.T.T.O when present.
	BuilderBaseBuilders int
	// Lab reports whether the Laboratory exists (research is impossible before).
	Lab bool
	// Pets is how many pets the account has unlocked (0 before TH14).
	Pets int
	// ReadAt is when the game produced the export.
	ReadAt time.Time
}

// Path is where the imported copy lives.
func Path() string { return paths.ResolveConfig(fileNameExport) }

// building is one entry of the export's buildings array. A pointer id keeps a
// malformed entry from decoding as building zero.
type building struct {
	ID    *int `json:"data"`
	Level int  `json:"lvl"`
	Count int  `json:"cnt"`
}

// export is the shape this package needs from the game's JSON. Unknown fields
// are ignored on purpose: the game adds and removes some between updates, and a
// parser that refuses to read a newer export is worse than one that reads the
// fields it knows.
type export struct {
	Buildings  []building        `json:"buildings"`
	Buildings2 []building        `json:"buildings2"`
	Heroes     []json.RawMessage `json:"heroes"`
	Heroes2    []json.RawMessage `json:"heroes2"`
	Units2     []json.RawMessage `json:"units2"`
	Pets       []json.RawMessage `json:"pets"`
	Timestamp  int64             `json:"timestamp"`
}

// Parse reads a copied export. It fails when the text is not the game's export
// (no buildings array, or no Town Hall in it), because silently importing
// something else would size the whole farm off a wrong Town Hall.
func Parse(raw []byte) (*Village, error) {
	var e export
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("not a village export: %w", err)
	}
	if len(e.Buildings) == 0 {
		return nil, fmt.Errorf("not a village export: no \"buildings\" array")
	}

	type levelCount struct{ level, count int }
	merged := make(map[int]levelCount, len(e.Buildings))
	for _, b := range e.Buildings {
		if b.ID == nil {
			continue
		}
		count := b.Count
		if count <= 0 {
			count = 1
		}
		old := merged[*b.ID]
		if b.Level > old.level {
			old.level = b.Level
		}
		old.count += count
		merged[*b.ID] = old
	}

	hall := merged[idTownHall]
	if hall.level < 1 {
		return nil, fmt.Errorf("not a village export: no Town Hall (data %d) in \"buildings\"", idTownHall)
	}

	base2 := make(map[int]bool, len(e.Buildings2))
	for _, b := range e.Buildings2 {
		if b.ID != nil {
			base2[*b.ID] = true
		}
	}

	v := &Village{
		TownHall:    hall.level,
		Lab:         merged[idLaboratory].count > 0,
		Pets:        len(e.Pets),
		Builders:    merged[idBuildersHut].count,
		BuilderBase: len(e.Buildings2) > 0 || len(e.Units2) > 0 || len(e.Heroes2) > 0,
	}
	if v.Builders > 0 && base2[idBobControl] {
		v.Builders++
	}
	if v.BuilderBase {
		v.BuilderBaseBuilders = 1 // the Master Builder
		if base2[idOttoOutpost] {
			v.BuilderBaseBuilders++
		}
	}
	if e.Timestamp > 0 {
		v.ReadAt = time.Unix(e.Timestamp, 0)
	}
	return v, nil
}

// Save parses raw and stores it as this profile's export, replacing any
// previous copy atomically so an interrupted import cannot leave a truncated
// file that the next boot would read as "no village".
func Save(raw []byte) (*Village, error) {
	v, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	path := Path()
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return nil, fmt.Errorf("write village export: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("replace village export: %w", err)
	}
	return v, nil
}

// Load reads the imported export. A missing file is reported as an error so the
// caller can tell "not imported" from "imported and unreadable", but neither is
// fatal: the bot farms without it.
func Load() (*Village, error) {
	raw, err := os.ReadFile(Path())
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Age reports how old the export is. Stale facts are still useful for the Town
// Hall (it only goes up), but the caller should say how old they are.
func (v *Village) Age(now time.Time) time.Duration {
	if v == nil || v.ReadAt.IsZero() {
		return 0
	}
	return now.Sub(v.ReadAt)
}
