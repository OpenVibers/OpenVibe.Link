package stream

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os/exec"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/video"
)

// PrivateScreenText is what the desk shows while the curtain is up. Viewers never see the reason, only this.
const PrivateScreenText = "Private screen"

// MakeCurtain returns the picture and the pre-encoded frames the desk substitutes while its curtain is up. It uses
// ffmpeg when it is installed (the words "Private screen" on a dark screen) and falls back to a black frame, which is
// still a blank: the curtain never fails open.
func MakeCurtain(ctx context.Context, ffmpeg string, w, h int) ([]video.Sample, []byte) {
	w, h = even(w), even(h)
	if w <= 0 || h <= 0 {
		w, h = 1280, 720
	}
	if w > 1920 {
		h = h * 1920 / w
		w = 1920
	}
	if h > 1080 {
		w = w * 1080 / h
		h = 1080
	}
	w, h = even(w), even(h)
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if path, err := exec.LookPath(ffmpeg); err == nil {
		if frames, err := ffmpegCurtainFrames(ctx, path, w, h); err == nil && len(frames) > 0 {
			if jpg, err := ffmpegCurtainJPEG(ctx, path, w, h); err == nil && len(jpg) > 0 {
				return frames, jpg
			}
			return frames, blackJPEG(w, h)
		}
		if jpg, err := ffmpegCurtainJPEG(ctx, path, w, h); err == nil && len(jpg) > 0 {
			return blackFrames(w, h), jpg
		}
	}
	return blackFrames(w, h), blackJPEG(w, h)
}

func drawtext(w, h int) string {
	size := h / 18
	if size < 14 {
		size = 14
	}
	return fmt.Sprintf("drawtext=text='%s':fontcolor=0xe8e8ee:fontsize=%d:x=(w-text_w)/2:y=(h-text_h)/2",
		PrivateScreenText, size)
}

func ffmpegCurtainFrames(ctx context.Context, ffmpeg string, w, h int) ([]video.Sample, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x101014:s=%dx%d:r=10", w, h), "-t", "1",
		"-vf", drawtext(w, h),
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "main",
		"-pix_fmt", "yuv420p", "-bf", "0", "-g", "10", "-f", "h264", "pipe:1"}
	out, err := exec.CommandContext(ctx, ffmpeg, args...).Output()
	if err != nil {
		return nil, err
	}
	var first *video.Sample
	err = video.AccessUnits(bytes.NewReader(out), func(au []byte, key bool) {
		if first == nil && key {
			cp := append([]byte(nil), au...)
			first = &video.Sample{Data: cp, Key: true, Duration: 100 * time.Millisecond}
		}
	})
	if err != nil {
		return nil, err
	}
	if first == nil {
		return nil, fmt.Errorf("no keyframe in the curtain")
	}
	return []video.Sample{*first}, nil
}

func ffmpegCurtainJPEG(ctx context.Context, ffmpeg string, w, h int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x101014:s=%dx%d", w, h), "-frames:v", "1",
		"-vf", drawtext(w, h), "-c:v", "mjpeg", "-q:v", "5", "-f", "image2pipe", "pipe:1"}
	return exec.CommandContext(ctx, ffmpeg, args...).Output()
}

func blackFrames(w, h int) []video.Sample {
	enc, err := video.NewPCMEncoder(w, h)
	if err != nil {
		return nil
	}
	f := video.NewFrame(w, h)
	for i := range f.Y {
		f.Y[i] = 16
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = 128, 128
	}
	return []video.Sample{{Data: enc.Encode(f, true, nil), Key: true, Duration: 100 * time.Millisecond}}
}

func blackJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// A dark, quiet card; the planner draws the words when ffmpeg is there.
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 16, 16, 20, 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return buf.Bytes()
}
