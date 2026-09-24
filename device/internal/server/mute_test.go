package server

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/wilbowes/EchoMuse/internal/bindings/mixer"
	"github.com/wilbowes/EchoMuse/pkg/led"
)

// adcRecorder counts writes to the ADC mute controls, which is what separates
// the hardware mute from Home Assistant's virtual one.
type adcRecorder struct {
	mu     sync.Mutex
	writes map[string]int // value → count
}

func (r *adcRecorder) Set(name string, values []string) error {
	if strings.HasPrefix(name, "ADC_") && strings.HasSuffix(name, " Mute") {
		r.mu.Lock()
		r.writes[values[0]]++
		r.mu.Unlock()
	}
	return nil
}

func (r *adcRecorder) Get(string) (string, error) { return "0", nil }

// noMixer stands in for the host build's absent backend once a test is done.
type noMixer struct{}

func (noMixer) Set(name string, _ []string) error { return fmt.Errorf("no mixer: %q", name) }
func (noMixer) Get(name string) (string, error)   { return "", fmt.Errorf("no mixer: %q", name) }

func newTestMute(t *testing.T) (*muteController, *adcRecorder, *[]bool) {
	t.Helper()
	rec := &adcRecorder{writes: map[string]int{}}
	mixer.Use(rec)
	t.Cleanup(func() { mixer.Use(noMixer{}) })
	var reports []bool
	m := newMuteController(func() led.Controller { return nil }, func(muted bool) {
		reports = append(reports, muted)
	})
	return m, rec, &reports
}

func TestRemoteMuteIsVirtual(t *testing.T) {
	m, rec, reports := newTestMute(t)
	m.SetRemote(true)
	if !m.IsMuted() || m.IsHardwareMuted() || !m.IsRemoteMuted() {
		t.Fatalf("after remote mute: muted=%v hw=%v remote=%v",
			m.IsMuted(), m.IsHardwareMuted(), m.IsRemoteMuted())
	}
	if len(rec.writes) != 0 {
		t.Errorf("remote mute wrote the ADC: %v", rec.writes)
	}
	if len(*reports) != 1 || !(*reports)[0] {
		t.Errorf("reports = %v, want [true]", *reports)
	}
	if m.SetRemote(true) {
		t.Error("a repeated remote mute reported a change")
	}
	if len(*reports) != 1 {
		t.Error("a repeated remote mute must not report again")
	}
}

// The reason the remote mute exists as a second flag: HA must not be able to
// reopen a mic closed at the button.
func TestRemoteUnmuteCannotClearTheButton(t *testing.T) {
	m, rec, reports := newTestMute(t)
	m.Toggle() // button: hardware mute
	m.SetRemote(true)
	m.SetRemote(false)
	if !m.IsMuted() || !m.IsHardwareMuted() {
		t.Fatal("a remote unmute reopened a mic muted at the button")
	}
	if rec.writes["0"] != 0 {
		t.Errorf("remote unmute wrote the ADC open %d times", rec.writes["0"])
	}
	for i, r := range *reports {
		if !r {
			t.Errorf("report %d said unmuted while the button mute held", i)
		}
	}
}

// A press on a muted device opens the mic, whichever mute closed it: the ring
// is red either way, and the person at the device outranks HA.
func TestButtonClearsBothMutes(t *testing.T) {
	for _, hw := range []bool{false, true} {
		m, _, _ := newTestMute(t)
		if hw {
			m.Toggle()
		}
		m.SetRemote(true)
		m.Toggle()
		if m.IsMuted() || m.IsHardwareMuted() || m.IsRemoteMuted() {
			t.Errorf("hw=%v: button press left muted=%v hw=%v remote=%v",
				hw, m.IsMuted(), m.IsHardwareMuted(), m.IsRemoteMuted())
		}
	}
}

func TestButtonOnOpenMicIsTheHardwareMute(t *testing.T) {
	m, rec, _ := newTestMute(t)
	m.Toggle()
	if !m.IsHardwareMuted() || m.IsRemoteMuted() {
		t.Fatal("button press did not set the hardware mute alone")
	}
	if rec.writes["1"] != len(adcMuteCtls) {
		t.Errorf("ADC mute writes = %d, want %d", rec.writes["1"], len(adcMuteCtls))
	}
}

func TestRemoteMutePersists(t *testing.T) {
	m, _, _ := newTestMute(t)
	persisted := 0
	m.persist = func() { persisted++ }
	m.SetRemote(true)
	m.SetRemote(false)
	if persisted != 2 {
		t.Errorf("persisted %d times, want 2", persisted)
	}
}
