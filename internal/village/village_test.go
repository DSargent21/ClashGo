package village

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleExport = `{
  "tag": "#ABCD1234",
  "timestamp": 1794000000,
  "buildings": [
    {"data": 1000001, "lvl": 12, "cnt": 1},
    {"data": 1000007, "lvl": 9, "cnt": 1},
    {"data": 1000015, "lvl": 5, "cnt": 5},
    {"data": 1000010, "lvl": 12, "cnt": 325}
  ],
  "buildings2": [
    {"data": 1000065, "lvl": 1, "cnt": 1},
    {"data": 1000078, "lvl": 1, "cnt": 1}
  ],
  "units2": [{"data": 4000000, "lvl": 10, "cnt": 20}],
  "pets": [{"data": 7300000, "lvl": 10}, {"data": 7300001, "lvl": 8}]
}`

func TestParseAccountFacts(t *testing.T) {
	v, err := Parse([]byte(sampleExport))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if v.TownHall != 12 {
		t.Fatalf("town hall = %d, want 12", v.TownHall)
	}
	if v.Builders != 6 {
		t.Fatalf("builders = %d, want 6 (5 huts + B.O.B)", v.Builders)
	}
	if !v.BuilderBase || v.BuilderBaseBuilders != 2 {
		t.Fatalf("builder base = %v with %d builders, want true with 2 (master + O.T.T.O)", v.BuilderBase, v.BuilderBaseBuilders)
	}
	if !v.Lab {
		t.Fatal("lab = false, want true: TH12 with a laboratory in the export")
	}
	if v.Pets != 2 {
		t.Fatalf("pets = %d, want 2", v.Pets)
	}
	if got := v.ReadAt.UTC().Format(time.RFC3339); got != time.Unix(1794000000, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("read at = %s, want the export timestamp", got)
	}
	if age := v.Age(v.ReadAt.Add(2 * time.Hour)); age != 2*time.Hour {
		t.Fatalf("age = %v, want 2h", age)
	}
}

func TestParseMergesDuplicateBuildingEntries(t *testing.T) {
	raw := `{"buildings":[{"data":1000001,"lvl":3,"cnt":1},{"data":1000001,"lvl":5,"cnt":1}]}`
	v, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if v.TownHall != 5 {
		t.Fatalf("town hall = %d, want the highest level seen (5)", v.TownHall)
	}
}

func TestParseRejectsNonExports(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"garbage", "not json", "not a village export"},
		{"empty object", "{}", `no "buildings"`},
		{"buildings without a town hall", `{"buildings":[{"data":1000015,"lvl":2,"cnt":4}]}`, "no Town Hall"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected an error: importing something that is not the export sizes the whole farm off a wrong Town Hall")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	if _, err := Load(); err == nil {
		t.Fatal("Load must fail before anything was imported")
	}

	saved, err := Save([]byte(sampleExport))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if loaded.TownHall != saved.TownHall || loaded.Builders != saved.Builders {
		t.Fatalf("loaded %+v, want %+v", loaded, saved)
	}
	if filepath.Dir(Path()) == "" {
		t.Fatal("Path resolved to nothing")
	}

	// A second import replaces the first, and the temp file must not survive.
	if _, err := Save([]byte(`{"buildings":[{"data":1000001,"lvl":9,"cnt":1}]}`)); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	again, err := Load()
	if err != nil {
		t.Fatalf("Load after re-import: %v", err)
	}
	if again.TownHall != 9 {
		t.Fatalf("town hall = %d, want the re-imported 9", again.TownHall)
	}
}
