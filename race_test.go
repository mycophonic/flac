//go:build race

package flac_test

// raceDetector reports a build under the race detector, whose instrumentation
// allocates beside the code under test; allocation bounds allow for it.
const raceDetector = true
