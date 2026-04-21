//go:build windows

package util

// TestPollutionResult holds counts of cleaned items from test pollution cleanup.
// On Windows, test pollution cleanup is not supported.
type TestPollutionResult struct {
	RogueDolt     int
	StaleDirs     int
	StalePIDs     int
	DeadWorktrees int
	Errors        []string
}

// String returns a one-line summary. On Windows this is always "clean".
func (r TestPollutionResult) String() string {
	return "Test pollution cleanup: clean (Windows — not supported)"
}

// CleanTestPollution is a Windows stub that performs no cleanup.
func CleanTestPollution(townRoot string) TestPollutionResult {
	return TestPollutionResult{}
}
