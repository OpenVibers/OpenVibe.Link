package voice

import (
	"math"
	"sync"
)

const (
	// DuckDB is how far the desk's own sound drops while the voice plays (the design's -12 dB).
	DuckDB = 12.0
	// RecoverMS is how long the desk's own sound takes to come back after the voice stops.
	RecoverMS = 300
	// Rate is the sample rate the mix works at; the Opus track is 48 kHz.
	Rate = 48000
)

// Mixer mixes the voice with the desk's own sound. While the voice plays the desk's sound is ducked by DuckDB, and it
// comes back over RecoverMS after the voice stops (the design's rule).
type Mixer struct {
	mu       sync.Mutex
	speaking bool
	gain     float64
	level    float64
}

// NewMixer returns a mixer with the desk's sound at full level.
func NewMixer() *Mixer {
	return &Mixer{gain: 1, level: 1}
}

// duckGain is the level the desk's own sound holds while the voice speaks.
func duckGain() float64 {
	return math.Pow(10, -DuckDB/20.0)
}

// SetVoice says whether the voice is speaking now.
func (m *Mixer) SetVoice(on bool) {
	m.mu.Lock()
	m.speaking = on
	m.mu.Unlock()
}

// Gain is the desk's sound level right now: 1 normally, the ducked level while the voice plays.
func (m *Mixer) Gain() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.speaking {
		return duckGain()
	}
	return m.level
}

// Mix adds the voice to the desk's own sound and returns the mix. The desk's sound is ducked while the voice speaks
// and comes back over RecoverMS once it stops.
func (m *Mixer) Mix(desk []int16, voice []int16) []int16 {
	n := len(desk)
	if len(voice) > n {
		n = len(voice)
	}
	out := make([]int16, n)
	m.mu.Lock()
	target := 1.0
	if m.speaking {
		target = duckGain()
	}
	step := (1.0 - duckGain()) / (RecoverMS * Rate / 1000)
	for i := 0; i < n; i++ {
		if !m.speaking && m.level < target {
			m.level += step
			if m.level > 1 {
				m.level = 1
			}
		} else if m.speaking && m.level > target {
			m.level = target
		}
		var d int16
		if i < len(desk) {
			d = desk[i]
		}
		var v int16
		if i < len(voice) {
			v = voice[i]
		}
		out[i] = int16(float64(d)*m.level + float64(v))
	}
	m.mu.Unlock()
	return out
}

// Frames splits the mix into 20 ms frames of 48 kHz stereo interleaved PCM, the shape the Opus encoder wants.
func (m *Mixer) Frames(pcm []int16, rate int) [][]int16 {
	if rate <= 0 {
		rate = Rate
	}
	if rate != Rate {
		pcm = resample(pcm, rate, Rate)
	}
	perFrame := Rate / 50 * 2 // 20 ms of stereo samples
	var frames [][]int16
	for i := 0; i+perFrame <= len(pcm); i += perFrame {
		frames = append(frames, pcm[i:i+perFrame])
	}
	return frames
}

// resample changes the sample rate of a mono stream with a plain linear interpolation: the voice is speech, and the
// desk's track is 48 kHz.
func resample(pcm []int16, from, to int) []int16 {
	if from == to || len(pcm) == 0 {
		return pcm
	}
	outLen := len(pcm) * to / from
	out := make([]int16, outLen)
	for i := 0; i < outLen; i++ {
		pos := float64(i) * float64(from) / float64(to)
		i0 := int(pos)
		if i0 >= len(pcm)-1 {
			out[i] = pcm[len(pcm)-1]
			continue
		}
		frac := pos - float64(i0)
		out[i] = int16(float64(pcm[i0])*(1-frac) + float64(pcm[i0+1])*frac)
	}
	return out
}
