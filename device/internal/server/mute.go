package server

import (
	"log"
	"sync"

	internalLed "github.com/wilbowes/EchoMuse/internal/bindings/led"
	"github.com/wilbowes/EchoMuse/internal/bindings/mixer"
	"github.com/wilbowes/EchoMuse/pkg/led"
)

type muteController struct {
	mu    sync.Mutex
	muted bool // the physical button's mute: ADC muted, button LED lit
	// remote is Home Assistant's mute, and it is VIRTUAL: the mic stream is
	// stopped and the ring shows red, but the ADC and the button LED are left
	// alone. It exists so HA can take the mic back as well as give it up
	// without being able to undo the physical button — a remote unmute
	// clears only this flag, so a device muted at the button stays muted.
	// Everything that asks IsMuted() sees the OR of the two, which is what
	// keeps mic_start refused, wakes suppressed and the ring red.
	remote  bool
	ledCtrl func() led.Controller
	// dotMuted is set externally to block dot button events while muted
	onMuteChange func(muted bool)
	// persist, when set, is called after every Toggle() so the mute state
	// survives reboots and OTA restarts (state.json). Separate from
	// onMuteChange: that one is the controller-notification hook wired by
	// cmd, this one is internal.
	persist func()
}

func newMuteController(ledGetter func() led.Controller, onMuteChange func(muted bool)) *muteController {
	return &muteController{
		ledCtrl:      ledGetter,
		onMuteChange: onMuteChange,
	}
}

// SetOnMuteChange wires a callback invoked when mute state changes.
// B7 fix (2026-07-05 review): previously Server.SetMuteChangeCallback
// reached directly into m.mu/m.onMuteChange from outside this struct.
// Encapsulating the lock here keeps muteController responsible for its
// own synchronisation, matching every other muteController method.
func (m *muteController) SetOnMuteChange(cb func(muted bool)) {
	m.mu.Lock()
	m.onMuteChange = cb
	m.mu.Unlock()
}

// IsMuted is the effective state: muted at the button OR from HA.
func (m *muteController) IsMuted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.muted || m.remote
}

// IsHardwareMuted is the physical button's mute alone.
func (m *muteController) IsHardwareMuted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.muted
}

// IsRemoteMuted is HA's virtual mute alone.
func (m *muteController) IsRemoteMuted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remote
}

// Toggle is the physical button. A press on a device muted either way opens
// the mic and clears BOTH mutes: the ring is red whichever set it, and the
// person pressing the button at the device outranks Home Assistant. A press
// on an open mic is the hardware mute.
func (m *muteController) Toggle() {
	m.mu.Lock()
	if m.muted || m.remote {
		m.muted, m.remote = false, false
	} else {
		m.muted = true
	}
	muted := m.muted
	// Copy under the lock — SetOnMuteChange writes this field under mu from
	// the main goroutine, and button events can fire before that wiring
	// completes (SubscribeToButton starts the evdev goroutines first).
	cb := m.onMuteChange
	persist := m.persist
	m.mu.Unlock()

	if muted {
		m.applyMute()
	} else {
		m.applyUnmute()
	}
	if persist != nil {
		persist()
	}

	if cb != nil {
		cb(muted)
	}
}

// SetRemote sets HA's virtual mute. It never touches the hardware mute, so a
// remote unmute on a device muted at the button changes nothing the person at
// the device can see. The change callback fires on every change of the flag,
// not only of the effective state, because the controller reports the flag
// itself to HA; its handler is idempotent in the effective state. Returns
// whether the flag changed, so the caller can answer a no-op itself.
func (m *muteController) SetRemote(on bool) bool {
	m.mu.Lock()
	if m.remote == on {
		m.mu.Unlock()
		return false
	}
	before := m.muted || m.remote
	m.remote = on
	after := m.muted || m.remote
	cb := m.onMuteChange
	persist := m.persist
	m.mu.Unlock()

	if after != before {
		if after {
			log.Println("Mute: mic muted remotely")
			m.showMuteLEDs()
		} else {
			log.Println("Mute: mic unmuted remotely")
			m.clearLEDs()
		}
	}
	if persist != nil {
		persist()
	}
	if cb != nil {
		cb(after)
	}
	return true
}

// RestoreRemoteMuted re-applies a persisted remote mute at boot. Flag only:
// the mute is virtual, and the ring is painted with the LED init.
func (m *muteController) RestoreRemoteMuted() {
	m.mu.Lock()
	m.remote = true
	m.mu.Unlock()
	log.Println("Mute: restoring persisted remote mute")
}

// adcMuteCtls are the per-chip ADC mute controls, all four codecs
// (A: ch0/ch1 … D: ch6 + unused). C5 hardware fix (2026-07-07): only chip
// A was muted before, leaving chips B–D — including ch6, the mic wake word
// and STT actually use — physically hot; the mic stream-stop was what made
// mute effective. By name since 2026-09-17 (#546).
var adcMuteCtls = []string{
	"ADC_A Left Mute", "ADC_A Right Mute",
	"ADC_B Left Mute", "ADC_B Right Mute",
	"ADC_C Left Mute", "ADC_C Right Mute",
	"ADC_D Left Mute", "ADC_D Right Mute",
}

// setAdcMute reports every failure, not just the first per control: this is
// the hardware half of the mute, and a silent miss here is a hot microphone.
func setAdcMute(val string) {
	failed := 0
	for _, ctl := range adcMuteCtls {
		if mixer.Set(ctl, val) != nil {
			failed++
		}
	}
	if failed > 0 {
		log.Printf("Mute: %d of %d ADC mute controls failed to set %s", failed, len(adcMuteCtls), val)
	}
}

// RestoreMuted re-applies a persisted muted state at boot: flag + ADC mute
// only. The LED hardware isn't up yet when this runs (NewServer, before the
// LED-init goroutine finishes), so the red ring and button LED are painted
// by that goroutine once the controllers exist.
func (m *muteController) RestoreMuted() {
	m.mu.Lock()
	m.muted = true
	m.mu.Unlock()
	log.Println("Mute: restoring persisted muted state")
	setAdcMute("1")
}

func (m *muteController) applyMute() {
	log.Println("Mute: mic muted")
	setAdcMute("1")
	m.showMuteLEDs()
	setMuteButtonLED(true)
}

func (m *muteController) applyUnmute() {
	log.Println("Mute: mic unmuted")
	setAdcMute("0")
	m.clearLEDs()
	setMuteButtonLED(false)
}

// setMuteButtonLED drives the discrete red LED under the mic-off button —
// stock-Alexa parity: the button itself shows muted, not just the ring.
// GPIO-backed and independent of the ring driver, so it needs no repaint
// protection (ring repaints can't stomp it) and survives every LED-mode
// transition for free. Direct binding call, same precedent as setAdcMute's
// tinymix exec above.
func setMuteButtonLED(on bool) {
	if err := internalLed.SetMuteButtonLED(on); err != nil {
		log.Printf("Mute button LED: %v", err)
	}
}

func (m *muteController) showMuteLEDs() {
	lc := m.ledCtrl()
	if lc == nil {
		return
	}
	leds := make([]led.Led, numLEDs)
	for i := 0; i < numLEDs; i++ {
		leds[i] = led.Led{ID: i, R: 180, G: 0, B: 0} // red ring
	}
	if err := lc.SetLEDs(leds...); err != nil {
		log.Printf("Mute LED set failed: %v", err)
	}
}

func (m *muteController) clearLEDs() {
	lc := m.ledCtrl()
	if lc == nil {
		return
	}
	clearLeds(lc)
}
