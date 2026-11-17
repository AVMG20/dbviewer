package main

import (
	"encoding/json"
	"sync"
	"time"

	"dbviewer/internal/store"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const (
	defaultWidth  = 1400
	defaultHeight = 900
	minWidth      = 960
	minHeight     = 600
	windowStateUI = "ui:window"
)

type windowState struct {
	X, Y, Width, Height int
	Maximised           bool
}

// appMenu is the standard macOS application menu. The Edit roles send the
// standard selectors (undo:, copy:, paste:, selectAll: ...) to the focused
// web view, so Cmd+C/V/X/A/Z work in text inputs. Key events reach the web page
// first: frontend handlers that call preventDefault (grid Cmd+Z, Cmd+S, Cmd+K,
// Cmd+R ...) win over the menu accelerators.
func appMenu(app *application.App) *application.Menu {
	m := application.NewMenu()
	m.AddRole(application.AppMenu)
	m.AddRole(application.EditMenu)
	view := m.AddSubmenu("View")
	if devBuild {
		view.AddRole(application.Reload)
		view.AddRole(application.OpenDevTools)
		view.AddSeparator()
	}
	view.AddRole(application.ToggleFullscreen)
	m.AddRole(application.WindowMenu)
	return m
}

// savedWindowState returns the saved window state (ok=false if none/invalid).
func savedWindowState(st *store.Store) (windowState, bool) {
	raw, _ := st.UIState(windowStateUI)
	var s windowState
	if raw == "" || json.Unmarshal([]byte(raw), &s) != nil || s.Width < minWidth || s.Height < minHeight {
		return s, false
	}
	return s, true
}

// applyWindowOptions sets the initial size/position from the saved state.
func applyWindowOptions(st *store.Store, o *application.WebviewWindowOptions) {
	s, ok := savedWindowState(st)
	if !ok {
		o.InitialPosition = application.WindowCentered
		return
	}
	o.Width, o.Height = s.Width, s.Height
	o.InitialPosition, o.X, o.Y = application.WindowXY, s.X, s.Y
	if s.Maximised {
		o.StartState = application.WindowStateMaximised
	}
}

// ensureOnScreen centers the window when the restored position is no longer on
// any screen (e.g. an external display was disconnected).
func ensureOnScreen(app *application.App, w *application.WebviewWindow) {
	x, y := w.Position()
	width, _ := w.Size()
	screens := app.Screen.GetAll()
	if len(screens) == 0 {
		return
	}
	for _, sc := range screens {
		b := sc.Bounds
		if x+width-100 > b.X && x+100 < b.X+b.Width && y+40 > b.Y && y+40 < b.Y+b.Height {
			return
		}
	}
	w.Center()
}

// trackWindowState saves size and position (debounced) when they change.
func trackWindowState(st *store.Store, w *application.WebviewWindow) {
	var mu sync.Mutex
	var timer *time.Timer
	save := func() {
		if w.IsFullscreen() || w.IsMinimised() {
			return
		}
		s := windowState{Maximised: w.IsMaximised()}
		if !s.Maximised {
			s.X, s.Y = w.Position()
			s.Width, s.Height = w.Size()
		} else if raw, _ := st.UIState(windowStateUI); raw != "" {
			// keep the restored (un-maximised) bounds
			var prev windowState
			if json.Unmarshal([]byte(raw), &prev) == nil {
				s.X, s.Y, s.Width, s.Height = prev.X, prev.Y, prev.Width, prev.Height
			}
		}
		if s.Width == 0 || s.Height == 0 {
			return
		}
		if b, err := json.Marshal(s); err == nil {
			_ = st.SetUIState(windowStateUI, string(b))
		}
	}
	schedule := func(*application.WindowEvent) {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(400*time.Millisecond, save)
	}
	w.OnWindowEvent(events.Common.WindowDidResize, schedule)
	w.OnWindowEvent(events.Common.WindowDidMove, schedule)
	w.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) { save() })
}
