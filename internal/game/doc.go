// Package game provides Clash of Clans screen state detection, navigation,
// and auto-exploration for the coc-bot automation system.
//
// Architecture
//
// The package implements a three-phase system:
//
//   Phase 1: Calibrator
//
// Call NewCalibrator(client).Calibrate() on startup to detect the BlueStacks
// screen resolution and compute scaling factors for all pixel coordinates.
// This makes the entire package resolution-independent.
//
//   Phase 2: Classifier
//
// NewClassifier(cal, cfg).ClassifyState(screen) runs every ~150ms and returns
// the current GameState with a confidence score. It uses pixel-based detection
// for speed (O(n) where n = number of rules, typically < 20) with no disk I/O.
//
//   Phase 3: Navigator / Explorer
//
// Navigator.Navigate(ctx, target) plans and executes the shortest path between
// any two states using Dijkstra's algorithm on the StateGraph. Explorer.Explore()
// auto-discovers all game states by clicking every interactive element and building
// a complete state transition map.
//
// GameContext
//
// The central shared type. All subsystems (training, attack, search) receive a
// *GameContext and read from it concurrently. The main capture loop writes to it.
//
// Quick Start
//
//	cal, _ := calibrator.Calibrate()
//	classifier := NewClassifier(cal, DefaultClassifierConfig())
//
//	classify := func(screen gocv.Mat) (GameState, int) {
//	    return classifier.ClassifyState(screen)
//	}
//
//	graph := NewStateGraph()
//	navigator := NewNavigator(client, cal, graph, classify)
//
//	ctx := NewGameContext()
//	for {
//	    screen, err := client.CaptureToMat()
//	    state, score := classify(screen)
//	    if classifier.ConfirmState(state) {
//	        ctx.UpdateState(state, time.Now())
//	    }
//	    ctx.UpdateScreen(screen, time.Since(start))
//	}
//
// State Machine
//
// GameState values cover every screen Clash of Clans can show. Each has an
// associated set of pixel checks compiled from the AutoIt reference bot.
// Detection priority orders states from most interruptive (dialogs, gem popup)
// to least (main village) so overlays never confuse the classifier.
//
// Template Extraction
//
// The Explorer saves cropped template images to assets/templates/ as it maps
// states. These can be used as a fallback for template matching when pixel
// checks are ambiguous. See TemplateStore for the matching API.
//
// Thread Safety
//
// GameContext uses a sync.RWMutex internally. Read methods (ReadScreen, ReadState,
// ReadHealth) are safe for concurrent use by any number of goroutines. Only
// the main capture loop writes to GameContext.
package game