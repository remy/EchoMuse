package speaker

import (
	"encoding/binary"
	"testing"
)

func constPeriod(frames int, v int16) []byte {
	b := make([]byte, frames*4)
	for i := 0; i < frames*2; i++ {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func sample(b []byte, frame, ch int) int16 {
	return int16(binary.LittleEndian.Uint16(b[frame*4+ch*2:]))
}

func TestOutputMuteUnmutedPassesThrough(t *testing.T) {
	var g outputMute
	in := constPeriod(64, 1000)
	out := g.apply(in, false, false)
	for f := 0; f < 64; f++ {
		if sample(out, f, 0) != 1000 || sample(out, f, 1) != 1000 {
			t.Fatalf("frame %d altered while unmuted", f)
		}
	}
}

func TestOutputMuteFadesOutThenSilences(t *testing.T) {
	var g outputMute
	out := g.apply(constPeriod(64, 16000), true, false)
	if out == nil {
		t.Fatal("the muting period must be faded, not dropped — a step is a click")
	}
	if s := sample(out, 0, 0); s < 15500 {
		t.Errorf("fade-out starts at %d, want near unity", s)
	}
	if sample(out, 63, 0) != 0 || sample(out, 63, 1) != 0 {
		t.Errorf("fade-out ends at %d/%d, want silence", sample(out, 63, 0), sample(out, 63, 1))
	}
	for f := 1; f < 64; f++ {
		if sample(out, f, 0) > sample(out, f-1, 0) {
			t.Fatalf("fade-out rises at frame %d", f)
		}
	}
	if g.apply(constPeriod(64, 16000), true, false) != nil {
		t.Error("steady mute must write silence")
	}
}

func TestOutputMuteFadesBackIn(t *testing.T) {
	g := outputMute{muted: true}
	out := g.apply(constPeriod(64, -16000), false, false)
	if sample(out, 0, 0) != 0 {
		t.Errorf("fade-in starts at %d, want silence", sample(out, 0, 0))
	}
	if s := sample(out, 63, 1); s > -15000 {
		t.Errorf("fade-in ends at %d, want near unity", s)
	}
	if g.muted {
		t.Error("state not recorded")
	}
}

// The shared silence period must never be written — a fade on it would
// corrupt every silent period after.
func TestOutputMuteNeverWritesSharedSilence(t *testing.T) {
	var g outputMute
	buf := constPeriod(64, 1234) // stands in for the shared period
	g.apply(buf, true, true)
	if sample(buf, 63, 0) != 1234 {
		t.Error("a period flagged silent was modified")
	}
	if !g.muted {
		t.Error("state must still change on a silent period")
	}
}
