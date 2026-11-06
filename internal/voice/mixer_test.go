package voice

import (
	"math"
	"testing"
)

// The design: the desk ducks its own sound by -12 dB while the voice plays, and it comes back 300 ms after.
func TestTheDeskSoundIsDuckedWhileTheVoicePlays(t *testing.T) {
	m := NewMixer()
	desk := make([]int16, 48000)
	for i := range desk {
		desk[i] = 1000
	}

	// Without the voice the desk sound is untouched.
	mixed := m.Mix(desk, nil)
	if mixed[0] != 1000 {
		t.Fatalf("the desk sound changed with no voice: %d", mixed[0])
	}

	m.SetVoice(true)
	mixed = m.Mix(desk, nil)
	want := int16(math.Round(1000 * math.Pow(10, -DuckDB/20.0)))
	if diff := int(mixed[0]) - int(want); diff > 2 || diff < -2 {
		t.Fatalf("the desk sound is %d, want about %d (-12 dB)", mixed[0], want)
	}

	// The voice is heard on top of the ducked desk sound.
	mixed = m.Mix(desk, []int16{500, 500})
	if mixed[0] <= want {
		t.Fatalf("the voice was not mixed in: %d", mixed[0])
	}

	// After the voice stops the desk sound is back within 300 ms.
	m.SetVoice(false)
	perFrame := Rate / 50
	recovered := false
	for i := 0; i < 3000; i++ {
		frame := make([]int16, perFrame)
		for j := range frame {
			frame[j] = 1000
		}
		out := m.Mix(frame, nil)
		if out[0] > 990 {
			recovered = true
			break
		}
	}
	if !recovered {
		t.Fatal("the desk sound did not come back within 300 ms")
	}
}

// The design: the frames are 20 ms of 48 kHz stereo, the shape the Opus track wants.
func TestTheFramesAreTwentyMilliseconds(t *testing.T) {
	m := NewMixer()
	pcm := make([]int16, 48000*2)
	frames := m.Frames(pcm, 48000)
	if len(frames) != 50 {
		t.Fatalf("got %d frames for one second, want 50", len(frames))
	}
	if len(frames[0]) != 1920 {
		t.Fatalf("a frame holds %d samples, want 1920 (20 ms of 48 kHz stereo)", len(frames[0]))
	}
}
