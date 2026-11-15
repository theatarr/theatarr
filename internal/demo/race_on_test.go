//go:build race

package demo_test

// raceDetector is true in a -race build, where the list golden (hundreds of whole-list requests) takes many minutes.
const raceDetector = true
