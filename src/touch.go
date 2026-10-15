package main

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	lua "github.com/yuin/gopher-lua"
)

// ---------------------------------------------------------------------------
// Touch / pointer input for menus and character select.
//
// The Android wrapper (ControllerOverlay) forwards finger events that are NOT on
// a fight control to SDL. This file turns the first finger (or the left mouse
// button on desktop, which makes testing easy) into:
//
//  1. Raw state for Lua (getTouch), already converted to motif coordinates.
//  2. "Virtual UI tokens" (touchPress) that uiRawInput() understands, so any
//     existing menu that calls getInput(-1, ...) reacts to touch without being
//     rewritten.
//  3. A generic fallback (tap = confirm, swipe = move) for screens that do not
//     claim the gesture through touchClaim().
// ---------------------------------------------------------------------------

// Mouse id SDL uses for events synthesized from touch (SDL_TOUCH_MOUSEID).
const touchMouseID uint32 = 0xFFFFFFFF


// Commands sent to the Android activity (SDLActivity.onUnhandledMessage).
const (
	mobileCmdMode   = 1 // param: 0 = menus, 1 = fight
	mobileCmdEditor = 2 // open the touch controls editor
)

type touchSys struct {
	down     bool
	pressed  bool // went down since the last frame
	released bool // went up since the last frame
	fingerID sdl.FingerID
	useMouse bool
	x, y     float32 // normalized window coordinates (0..1)

	// generic gesture tracking
	startX, startY   float32
	anchorX, anchorY float32
	moved            bool
	claimed          bool
	pendingTap       bool
	pendingTapFrame  int32

	// virtual UI tokens: token -> frame when it was queued
	tokens map[string]int32

	mode int // last mode sent to the Android activity
}

var touch = touchSys{tokens: map[string]int32{}, mode: -1}

// Called once per frame before events are polled.
func (t *touchSys) beginFrame() {
	t.pressed = false
	t.released = false
}

func (t *touchSys) onDown(x, y float32) {
	t.down = true
	t.pressed = true
	t.x, t.y = x, y
	t.startX, t.startY = x, y
	t.anchorX, t.anchorY = x, y
	t.moved = false
	t.claimed = false
	t.pendingTap = false
}

func (t *touchSys) onMove(x, y float32) {
	t.x, t.y = x, y
}

func (t *touchSys) onUp(x, y float32) {
	if !t.down {
		return
	}
	t.x, t.y = x, y
	t.down = false
	t.released = true
	t.useMouse = false
	if !t.moved && !t.claimed {
		// Give Lua one frame to claim a very fast tap before it becomes a generic "confirm".
		t.pendingTap = true
		t.pendingTapFrame = sys.frameCounter
	}
}

func (t *touchSys) queue(tok string) {
	t.tokens[tok] = sys.frameCounter
}

// Consumes a queued token if any of btns matches. Used by System.uiRawInput.
func (t *touchSys) take(btns []string, controllerIdx int) bool {
	if len(t.tokens) == 0 || controllerIdx > 0 {
		return false
	}
	for _, raw := range btns {
		tok := strings.TrimSpace(raw)
		if frame, ok := t.tokens[tok]; ok {
			delete(t.tokens, tok)
			if sys.frameCounter-frame <= 2 {
				return true
			}
		}
	}
	return false
}

// Generic gestures for screens that did not claim the touch. Runs once per frame after events.
func (t *touchSys) generic() {
	// Drop stale tokens
	for tok, frame := range t.tokens {
		if sys.frameCounter-frame > 3 {
			delete(t.tokens, tok)
		}
	}
	// Tap -> confirm (one frame after release, so Lua had a chance to claim it)
	if t.pendingTap && sys.frameCounter > t.pendingTapFrame {
		t.pendingTap = false
		if !t.claimed {
			t.queue("a")
		}
	}
	if !t.down || t.claimed {
		return
	}
	// Swipe -> directions
	dx := t.x - t.anchorX
	dy := t.y - t.anchorY
	const slop = 0.02
	if !t.moved {
		if math.Abs(float64(t.x-t.startX)) < slop && math.Abs(float64(t.y-t.startY)) < slop {
			return
		}
		t.moved = true
		t.pendingTap = false
	}
	const stepX, stepY = 0.07, 0.10
	if math.Abs(float64(dx)) >= stepX && math.Abs(float64(dx)) >= math.Abs(float64(dy)) {
		if dx > 0 {
			t.queue("R")
		} else {
			t.queue("L")
		}
		t.anchorX, t.anchorY = t.x, t.y
	} else if math.Abs(float64(dy)) >= stepY {
		if dy > 0 {
			t.queue("D")
		} else {
			t.queue("U")
		}
		t.anchorX, t.anchorY = t.x, t.y
	}
}

// ---------------------------------------------------------------------------
// SDL events
// ---------------------------------------------------------------------------

func touchNormalizePixels(px, py int32) (float32, float32) {
	w, h := float32(sys.scrrect[2]), float32(sys.scrrect[3])
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	return float32(px) / w, float32(py) / h
}

func touchOnFinger(e sdl.TouchFingerEvent) {
	switch e.Type {
	case sdl.FINGERDOWN:
		if !touch.down {
			touch.fingerID = e.FingerID
			touch.useMouse = false
			touch.onDown(e.X, e.Y)
		}
	case sdl.FINGERMOTION:
		if touch.down && !touch.useMouse && e.FingerID == touch.fingerID {
			touch.onMove(e.X, e.Y)
		}
	case sdl.FINGERUP:
		if touch.down && !touch.useMouse && e.FingerID == touch.fingerID {
			touch.onUp(e.X, e.Y)
		}
	}
}

// Real mouse only (touch-synthesized mouse events are ignored). Handy for testing on desktop.
func touchOnMouseButton(e sdl.MouseButtonEvent) {
	if e.Which == touchMouseID || e.Button != 1 /* SDL_BUTTON_LEFT */ {
		return
	}
	nx, ny := touchNormalizePixels(e.X, e.Y)
	if e.State == sdl.PRESSED {
		if !touch.down {
			touch.useMouse = true
			touch.onDown(nx, ny)
		}
	} else if touch.down && touch.useMouse {
		touch.onUp(nx, ny)
	}
}

func touchOnMouseMotion(e sdl.MouseMotionEvent) {
	if e.Which == touchMouseID || !touch.down || !touch.useMouse {
		return
	}
	nx, ny := touchNormalizePixels(e.X, e.Y)
	touch.onMove(nx, ny)
}

// ---------------------------------------------------------------------------
// Coordinates
// ---------------------------------------------------------------------------

// Converts normalized window coordinates to the motif's localcoord space. This mirrors
// TextSprite.SetLocalcoord and the offsets applied when texts are drawn.
func (s *System) touchToMotif(nx, ny float32) (float32, float32) {
	lx, ly := float32(s.motif.Info.Localcoord[0]), float32(s.motif.Info.Localcoord[1])
	gw, gh := s.gameWidth, s.gameHeight
	if lx <= 0 || ly <= 0 || gw <= 0 || gh <= 0 {
		return nx * 320, ny * 240
	}
	v := lx
	if lx*3 > ly*4 {
		v = ly * 4 / 3
	}
	localScale := 320 / v
	offsetX := -float32(int32((math.Floor(float64(lx/(v/320)-320))) / 2))
	baseX := nx*gw - (gw-320)/2 - offsetX
	baseY := ny*gh - (gh - 240)
	return baseX / localScale, baseY / localScale
}

// ---------------------------------------------------------------------------
// Android bridge
// ---------------------------------------------------------------------------

// Tells the Android wrapper which controls to show: 0 = menus (touch is forwarded here), 1 = fight (virtual pad).
func mobileSetMode(mode int) {
	if touch.mode == mode {
		return
	}
	touch.mode = mode
	mobileSend(mobileCmdMode, mode)
}

// ---------------------------------------------------------------------------
// Lua API
// ---------------------------------------------------------------------------

func touchScriptInit(l *lua.LState) {
	luaRegister(l, "getTouch", func(l *lua.LState) int {
		/*Get the state of the primary finger (or left mouse button).
		@function getTouch
		@treturn boolean down Finger is currently down.
		@treturn boolean pressed Finger went down since the last frame.
		@treturn boolean released Finger went up since the last frame.
		@treturn number mx X position in motif localcoord units.
		@treturn number my Y position in motif localcoord units.
		@treturn number nx X position, normalized to the window (0..1).
		@treturn number ny Y position, normalized to the window (0..1).*/
		mx, my := sys.touchToMotif(touch.x, touch.y)
		l.Push(lua.LBool(touch.down))
		l.Push(lua.LBool(touch.pressed))
		l.Push(lua.LBool(touch.released))
		l.Push(lua.LNumber(mx))
		l.Push(lua.LNumber(my))
		l.Push(lua.LNumber(touch.x))
		l.Push(lua.LNumber(touch.y))
		return 7
	})
	luaRegister(l, "touchClaim", func(l *lua.LState) int {
		/*Mark the current gesture as handled by a screen, which disables the generic tap/swipe fallback.
		@function touchClaim*/
		touch.claimed = true
		touch.pendingTap = false
		return 0
	})
	luaRegister(l, "touchPress", func(l *lua.LState) int {
		/*Queue a virtual UI key for one frame (a, m, U, D, L, R...). getInput() will see it.
		@function touchPress
		@tparam string token Key token.*/
		touch.queue(strArg(l, 1))
		return 0
	})
	luaRegister(l, "touchSetMode", func(l *lua.LState) int {
		/*Tell the Android wrapper whether the game is in menus (0) or in a fight (1).
		@function touchSetMode
		@tparam int mode 0 = menus, 1 = fight.*/
		mobileSetMode(int(numArg(l, 1)))
		return 0
	})
	luaRegister(l, "touchOpenEditor", func(l *lua.LState) int {
		/*Ask the Android wrapper to open the touch controls editor.
		@function touchOpenEditor*/
		mobileSend(mobileCmdEditor, 0)
		return 0
	})
}
