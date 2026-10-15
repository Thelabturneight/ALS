package main

import "math"

// ---------------------------------------------------------------------------
// Character volume normalization.
//
// Every sound is decoded once at load time (readSound) and its loudness is
// measured. The loudness of a character is the RMS of all its audible samples
// together. A gain is then applied to everything that character plays so that
// all characters end up at the same level (Config Sound.NormalizeTarget, dBFS).
// ---------------------------------------------------------------------------

const (
	normSilenceThreshold = 0.003 // about -50 dBFS; quieter samples are ignored
	normMinGain          = 0.35  // never reduce by more than about 9 dB
	normMaxGain          = 2.0   // never boost by more than 6 dB
	normMinFrames        = 4410  // ignore characters with less than ~0.1 s of audible sound
)

// Returns the integrated RMS loudness (dBFS) and the highest peak of an Snd.
func (s *Snd) loudness() (db float64, peak float32, ok bool) {
	if s == nil {
		return 0, 0, false
	}
	var sq float64
	var frames int
	for _, snd := range s.table {
		if snd == nil {
			continue
		}
		sq += snd.activeSq
		frames += snd.activeFrames
		if snd.peak > peak {
			peak = snd.peak
		}
	}
	if frames < normMinFrames || sq <= 0 {
		return 0, peak, false
	}
	rms := math.Sqrt(sq / float64(frames))
	return 20 * math.Log10(rms), peak, true
}

// Gain that brings a character to the configured target level. 1 means "unchanged".
func charNormGain(snd *Snd) float32 {
	if !sys.cfg.Sound.NormalizeChars {
		return 1
	}
	db, peak, ok := snd.loudness()
	if !ok {
		return 1
	}
	target := float64(sys.cfg.Sound.NormalizeTarget)
	if target >= 0 || target < -60 {
		target = -20
	}
	gain := math.Pow(10, (target-db)/20)
	gain = math.Max(normMinGain, math.Min(normMaxGain, gain))
	// Never boost into clipping
	if gain > 1 && peak > 0 {
		gain = math.Min(gain, 1/float64(peak))
		gain = math.Max(gain, 1)
	}
	return float32(gain)
}
