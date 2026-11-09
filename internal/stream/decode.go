package stream

import (
	"errors"
	"image"
	"strconv"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/OpenVibers/OpenVibe.Link/internal/video"
)

// DecodeProbe reads a probe frame's QR payload: the millisecond clock the desk drew it with. It is the same decode the
// headless viewer does (ZXing there; gozxing here), so the probe can be checked without a browser.
func DecodeProbe(f *video.Frame) (int64, error) {
	return decodeQRImage(frameGray(f))
}

func frameGray(f *video.Frame) image.Image {
	img := image.NewGray(image.Rect(0, 0, f.W, f.H))
	for y := 0; y < f.H; y++ {
		copy(img.Pix[y*img.Stride:y*img.Stride+f.W], f.Y[y*f.W:(y+1)*f.W])
	}
	return img
}

func decodeQRImage(img image.Image) (int64, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return 0, err
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return 0, err
	}
	ms, err := strconv.ParseInt(res.GetText(), 10, 64)
	if err != nil {
		return 0, errors.New("stream: the probe QR does not hold a millisecond clock")
	}
	return ms, nil
}
