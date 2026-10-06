package bot

import "testing"

// TestUpdatePromptDismissIsBounded pins the guard that keeps a prompt a touch
// cannot clear from becoming a tap storm.
//
// The "Update Available!" prompt exists because a newer CoC build does, and no
// touch can change that. If the outside-panel touch does not dismiss it, the
// capture loop would ask for another one on every frame: ~150 taps on the
// village behind the panel across the 5-minute boot grace, which is exactly the
// random tapping the dismissal path exists to remove. So a session spends at
// most maxUpdatePromptDismissAttempts, and then stops and says why.
func TestUpdatePromptDismissIsBounded(t *testing.T) {
	if maxUpdatePromptDismissAttempts < 1 {
		t.Fatalf("maxUpdatePromptDismissAttempts = %d; the prompt would never be dismissed at all",
			maxUpdatePromptDismissAttempts)
	}
	for attempt := 1; attempt <= maxUpdatePromptDismissAttempts; attempt++ {
		if !updatePromptDismissAllowed(attempt) {
			t.Errorf("attempt %d of %d refused: the bot would give up before spending the taps it is allowed",
				attempt, maxUpdatePromptDismissAttempts)
		}
	}
	if updatePromptDismissAllowed(maxUpdatePromptDismissAttempts + 1) {
		t.Errorf("attempt %d is allowed: the session would never stop tapping a prompt it cannot clear",
			maxUpdatePromptDismissAttempts+1)
	}
	if !updatePromptDismissAllowed(1) {
		t.Error("the first attempt of a session is refused; the prompt would never be dismissed")
	}
}
