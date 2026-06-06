package tool

func init() {
	// Disable sqz globally for tests to prevent it from interfering with shell command output checks,
	// except in specific tests where it is explicitly mocked.
	isSqzAvailable = func() bool { return false }
}
