package engine

// NewGrabber returns the platform capture backend.
// Platform constructors live in grabber_*.go (build-tagged).
func NewGrabber() Grabber {
	return newPlatformGrabber()
}
