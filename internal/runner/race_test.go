//go:build race

package runner_test

// raceEnabled: the race detector makes code several times slower, so
// timing checks run only in the build without it.
const raceEnabled = true
