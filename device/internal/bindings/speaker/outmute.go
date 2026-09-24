package speaker

// Speaker mute — Home Assistant's media player mute.
//
// No build tag, for mix.go's reason: pcm_speaker.go only compiles on the
// device, and this is the part worth testing on the host.
//
// Applied at the ALSA write rather than by the controller withholding audio,
// because the music plane runs seconds ahead of realtime and audio already on
// the device cannot be withheld. Muting keeps consuming both planes, so time
// passes as it would at any other volume: unmuting mid-song resumes the song
// where it has got to, and the controller's pacing never notices.
//
// Not the DAC volume control: its floor is −63.5dB rather than silence, and
// writing it would move the level the volume slider reports.

// outputMute is the gate on the ALSA goroutine. Single-consumer, like Mixer;
// the requested state is an atomic in PcmSpeaker.
type outputMute struct {
	muted bool // the state the last period was written in
}

// apply returns the period to write, or nil for silence. On the period where
// the state changes, out is faded across its length in place, since a gain
// step at a period boundary is a click. silent says out is the shared silence
// period, which must never be written and needs no fade.
func (g *outputMute) apply(out []byte, muted, silent bool) []byte {
	if muted == g.muted {
		if muted {
			return nil
		}
		return out
	}
	g.muted = muted
	if !silent {
		fadePeriod(out, !muted)
	}
	return out
}

// fadePeriod ramps interleaved stereo S16LE linearly across the buffer: from
// silence to unity when in is true, from unity to silence otherwise.
func fadePeriod(buf []byte, in bool) {
	frames := len(buf) / 4
	if frames == 0 {
		return
	}
	for f := 0; f < frames; f++ {
		n := f
		if !in {
			n = frames - 1 - f
		}
		g := int32(n) * unityGain / int32(frames)
		for c := 0; c < 2; c++ {
			i := f*4 + c*2
			s := int32(int16(uint16(buf[i]) | uint16(buf[i+1])<<8))
			s = s * g >> 15
			buf[i] = byte(s)
			buf[i+1] = byte(s >> 8)
		}
	}
}
