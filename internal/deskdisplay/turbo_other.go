//go:build !(linux && (amd64 || arm64))

package deskdisplay

import "errors"

// turbo is libjpeg-turbo's encoder where purego can load it (Linux on amd64 and arm64); elsewhere the Go encoder runs.
type turbo struct{}

func loadTurbo() (*turbo, error) {
	return nil, errors.New("libturbojpeg is loaded on Linux amd64 and arm64 only")
}

func (t *turbo) encode([]byte, int, int, int, int) ([]byte, error) {
	return nil, errors.New("no libturbojpeg")
}

// TurboJPEG says whether the fast encoder is in use here, and why not.
func TurboJPEG() (bool, string) { return false, "libturbojpeg is loaded on Linux amd64 and arm64 only" }
