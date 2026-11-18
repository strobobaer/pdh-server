//go:build !unix

package esppdh

// processAlive: außerhalb von Unix prüft TestRunnerTimeoutKillsChildren das Kind nicht.
func processAlive(int) bool { return false }
