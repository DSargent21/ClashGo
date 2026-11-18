package attack

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// The battle wait only needs the digit templates, but it used to decode every
// template in assets/templates to get them, once per battle. This measures the
// two setup shapes: the old owned-store path and the bot's shared store.
func BenchmarkBattleLootRecognizerSetup(b *testing.B) {
	shared, err := game.NewTemplateStore("../../assets/templates")
	if err != nil {
		b.Fatal(err)
	}
	if err := shared.LoadTemplates(); err != nil {
		shared.Close()
		b.Fatal(err)
	}
	defer shared.Close()
	cal := game.NewCalibration(1280, 720)
	logger := zerolog.Nop()

	b.Run("owned-store", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ts, err := game.NewTemplateStore("../../assets/templates")
			if err != nil {
				b.Fatal(err)
			}
			if err := ts.LoadTemplates(); err != nil {
				ts.Close()
				b.Fatal(err)
			}
			lr := game.NewLootRecognizer(cal, ts, logger)
			lr.Close()
			ts.Close()
		}
	})

	b.Run("shared-store", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			lr := game.NewLootRecognizer(cal, shared, logger)
			lr.Close()
		}
	})
}
