//go:build !race

package retention

// raceDetector is true in a -race build, which runs this package several times slower.
const raceDetector = false
