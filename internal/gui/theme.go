package gui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// The look of the window: two palettes (light and dark, or whichever the desktop uses), one
// style sheet written over them. Widgets say what they are with properties (role, kind), and
// the style sheet decides how that looks, so no page has colors of its own.

type theme struct {
	Dark                                                     bool
	Window, Sidebar, Surface, Raised, Hover, Border, Line    string
	Text, Text2, Text3                                       string
	Accent, AccentHover, OnAccent, AccentSoft                string
	OK, OKSoft, Warn, WarnSoft, Bad, BadSoft, Info, InfoSoft string
}

var (
	darkTheme = theme{Dark: true,
		Window: "#15161b", Sidebar: "#1b1c22", Surface: "#212229", Raised: "#2a2b33", Hover: "#2c2d36", Border: "#363742", Line: "#2a2b33",
		Text: "#ececf1", Text2: "#a9abb8", Text3: "#747684",
		Accent: "#8b93ff", AccentHover: "#a1a8ff", OnAccent: "#12131a", AccentSoft: "#2d2f52",
		OK: "#4cc38a", OKSoft: "#1d3a2d", Warn: "#f0b429", WarnSoft: "#3f3319", Bad: "#ff7b7b", BadSoft: "#472426", Info: "#62b0ff", InfoSoft: "#1d3350"}
	lightTheme = theme{
		Window: "#f5f5f8", Sidebar: "#ebebf1", Surface: "#ffffff", Raised: "#f0f0f5", Hover: "#e7e7ee", Border: "#d9dae3", Line: "#ececf2",
		Text: "#1b1c22", Text2: "#585a68", Text3: "#878997",
		Accent: "#4a55d1", AccentHover: "#3c46b8", OnAccent: "#ffffff", AccentSoft: "#e2e4fb",
		OK: "#16855a", OKSoft: "#d9f2e6", Warn: "#a26a10", WarnSoft: "#faeed2", Bad: "#c93a3a", BadSoft: "#fbe2e2", Info: "#2371c9", InfoSoft: "#dcebfb"}
)

// th is the theme in use. Only touched on the window's thread.
var th = darkTheme

// baseFontPt is the desktop's font size; the app's text sizes are relative to it.
var baseFontPt = 10.0

const styleSheet = `
* { outline: none; }
QWidget { color: @text; }
QMainWindow, QDialog, QStackedWidget, QStackedWidget > QWidget { background: @window; }
QToolTip { background: @raised; color: @text; border: 1px solid @border; padding: 5px 8px; border-radius: 6px; }

/* the sidebar */
#sidebar { background: @sidebar; border-right: 1px solid @line; }
QLabel[role="brand"] { font-size: @fsH1pt; font-weight: 700; }
QLabel[role="navhead"] { color: @text3; font-size: @fsSmallpt; font-weight: 600; padding: 12px 10px 3px 10px; }
QPushButton[nav="true"] { text-align: left; padding: 8px 10px; border: none; border-radius: 8px; background: transparent; color: @text2; font-weight: 500; }
QPushButton[nav="true"]:hover { background: @hover; color: @text; }
QPushButton[nav="true"]:checked { background: @accentsoft; color: @accent; font-weight: 600; }
QPushButton[nav="true"]:focus { border: 1px solid @accent; padding: 7px 9px; }
QLabel[role="badge"] { background: @accent; color: @onaccent; border-radius: 9px; padding: 0 6px; font-size: @fsSmallpt; font-weight: 700; }

/* text */
QLabel { background: transparent; }
QLabel[role="title"] { font-size: @fsTitlept; font-weight: 700; }
QLabel[role="h1"] { font-size: @fsH1pt; font-weight: 700; }
QLabel[role="h2"] { font-size: @fsH2pt; font-weight: 600; }
QLabel[role="subtitle"] { color: @text2; }
QLabel[role="hint"] { color: @text2; font-size: @fsSmallpt; }
QLabel[role="muted"] { color: @text3; font-size: @fsSmallpt; }
QLabel[role="stat"] { font-size: @fsTitlept; font-weight: 700; }
QLabel[role="error"] { color: @bad; font-size: @fsSmallpt; }
QLabel[role="pill"] { border-radius: 9px; padding: 1px 9px; font-size: @fsSmallpt; font-weight: 600; }
QLabel[pill="ok"] { background: @oksoft; color: @ok; }
QLabel[pill="warn"] { background: @warnsoft; color: @warn; }
QLabel[pill="bad"] { background: @badsoft; color: @bad; }
QLabel[pill="info"] { background: @infosoft; color: @info; }
QLabel[pill="accent"] { background: @accentsoft; color: @accent; }
QLabel[pill="off"] { background: @raised; color: @text3; }
QLabel[role="toast"] { background: @text; color: @window; border-radius: 10px; padding: 9px 16px; font-weight: 500; }

/* surfaces */
QFrame[role="card"] { background: @surface; border: 1px solid @line; border-radius: 12px; }
QFrame[role="pane"] { background: @surface; border-left: 1px solid @line; }
QFrame[role="banner"] { background: @infosoft; border-radius: 10px; }
QFrame[role="warnbanner"] { background: @warnsoft; border-radius: 10px; }
QFrame[role="line"] { background: @line; border: none; max-height: 1px; min-height: 1px; }
QScrollArea { background: transparent; border: none; }
QScrollArea > QWidget > QWidget { background: transparent; }

/* buttons */
QPushButton, QToolButton { background: @surface; border: 1px solid @border; border-radius: 8px; padding: 6px 14px; color: @text; font-weight: 500; }
QToolButton { padding: 5px; }
QPushButton:hover, QToolButton:hover { background: @hover; }
QPushButton:pressed, QToolButton:pressed { background: @border; }
QPushButton:focus, QToolButton:focus { border-color: @accent; }
QPushButton:disabled, QToolButton:disabled { color: @text3; background: @raised; border-color: @line; }
QPushButton[kind="primary"] { background: @accent; border-color: @accent; color: @onaccent; font-weight: 600; }
QPushButton[kind="primary"]:hover { background: @accenthover; border-color: @accenthover; }
QPushButton[kind="primary"]:focus { border-color: @text; }
QPushButton[kind="primary"]:disabled { background: @raised; border-color: @line; color: @text3; }
QPushButton[kind="ghost"], QToolButton[kind="ghost"] { background: transparent; border-color: transparent; color: @text2; }
QPushButton[kind="ghost"]:hover, QToolButton[kind="ghost"]:hover { background: @hover; color: @text; }
QPushButton[kind="ghost"]:focus, QToolButton[kind="ghost"]:focus { border-color: @accent; }
QPushButton[kind="link"] { background: transparent; border: none; color: @accent; padding: 2px 0; text-align: left; font-weight: 500; }
QPushButton[kind="link"]:hover { color: @accenthover; background: transparent; }
QPushButton[kind="danger"] { color: @bad; }
QPushButton[kind="danger"]:hover { background: @badsoft; }
QPushButton[kind="danger"]:disabled { color: @text3; background: @raised; border-color: @line; }
QPushButton[kind="chip"] { border-radius: 14px; padding: 4px 12px; background: @surface; color: @text2; }
QPushButton[kind="chip"]:hover { background: @hover; color: @text; }
QPushButton[kind="chip"]:checked { background: @accentsoft; border-color: @accent; color: @accent; }
QPushButton[kind="segment"] { border-radius: 7px; padding: 5px 14px; background: transparent; border-color: transparent; color: @text2; }
QPushButton[kind="segment"]:hover { background: @hover; color: @text; }
QPushButton[kind="segment"]:checked { background: @surface; border-color: @border; color: @text; }
QFrame[role="segments"] { background: @raised; border-radius: 9px; }

/* fields */
QLineEdit, QComboBox, QSpinBox, QDoubleSpinBox, QPlainTextEdit, QTextEdit {
  background: @surface; border: 1px solid @border; border-radius: 8px; padding: 5px 9px; color: @text;
  selection-background-color: @accent; selection-color: @onaccent; }
QLineEdit:focus, QComboBox:focus, QSpinBox:focus, QDoubleSpinBox:focus, QPlainTextEdit:focus, QTextEdit:focus { border-color: @accent; }
QLineEdit:disabled, QComboBox:disabled, QSpinBox:disabled, QDoubleSpinBox:disabled { color: @text3; background: @raised; border-color: @line; }
QLineEdit[role="bigsearch"] { padding: 10px 14px; border-radius: 10px; font-size: @fsH2pt; }
QComboBox:hover { background: @hover; }
QComboBox::drop-down { border: none; width: 24px; }
QComboBox::down-arrow { image: url(@chevdown); width: 12px; height: 12px; }
QComboBox QAbstractItemView { background: @surface; border: 1px solid @border; border-radius: 8px; padding: 4px; selection-background-color: @accentsoft; selection-color: @text; outline: none; }
QSpinBox::up-button, QDoubleSpinBox::up-button { subcontrol-origin: border; subcontrol-position: top right; width: 20px; border: none; }
QSpinBox::down-button, QDoubleSpinBox::down-button { subcontrol-origin: border; subcontrol-position: bottom right; width: 20px; border: none; }
QSpinBox::up-arrow, QDoubleSpinBox::up-arrow { image: url(@chevup); width: 10px; height: 10px; }
QSpinBox::down-arrow, QDoubleSpinBox::down-arrow { image: url(@chevdown); width: 10px; height: 10px; }
QCheckBox { spacing: 8px; }
QCheckBox::indicator { width: 17px; height: 17px; border-radius: 5px; border: 1px solid @border; background: @surface; }
QCheckBox::indicator:hover { border-color: @accent; }
QCheckBox::indicator:checked { background: @accent; border-color: @accent; image: url(@check); }
QCheckBox:focus { color: @accent; }

/* lists */
QListView, QTableView, QTreeView, QTableWidget, QListWidget {
  background: transparent; border: none; alternate-background-color: transparent;
  selection-background-color: @accentsoft; selection-color: @text; gridline-color: @line; }
QFrame[role="card"] QListView, QFrame[role="card"] QTableView { background: transparent; }
QTableView::item, QListWidget::item, QTreeView::item { padding: 3px 6px; border: none; }
QListWidget::item { border-radius: 7px; padding: 5px 8px; }
QListWidget::item:hover, QTableView::item:hover { background: @hover; }
QListWidget::item:selected, QTableView::item:selected { background: @accentsoft; color: @text; }
QHeaderView { background: transparent; }
QHeaderView::section { background: transparent; color: @text3; border: none; border-bottom: 1px solid @line; padding: 6px 6px; font-size: @fsSmallpt; font-weight: 600; }
QTableCornerButton::section { background: transparent; border: none; }
QPlainTextEdit[role="log"] { font-family: monospace; border-radius: 10px; }

QProgressBar { background: @raised; border: none; border-radius: 3px; max-height: 6px; min-height: 6px; text-align: center; color: transparent; }
QProgressBar::chunk { background: @accent; border-radius: 3px; }

QScrollBar:vertical { background: transparent; width: 11px; margin: 2px; }
QScrollBar:horizontal { background: transparent; height: 11px; margin: 2px; }
QScrollBar::handle { background: @border; border-radius: 3px; min-height: 28px; min-width: 28px; }
QScrollBar::handle:hover { background: @text3; }
QScrollBar::add-line, QScrollBar::sub-line, QScrollBar::add-page, QScrollBar::sub-page { background: transparent; border: none; width: 0; height: 0; }

QMenu { background: @surface; border: 1px solid @border; border-radius: 9px; padding: 5px; }
QMenu::item { padding: 6px 20px 6px 12px; border-radius: 6px; }
QMenu::item:selected { background: @accentsoft; color: @text; }
QMenu::item:disabled { color: @text3; }
QMenu::separator { height: 1px; background: @line; margin: 4px 6px; }
QSplitter::handle { background: transparent; }
QDialogButtonBox QPushButton { min-width: 72px; }
`

// sheet fills the style sheet in with a palette.
func sheet(t theme, chevDown, chevUp, check string) string {
	size := func(f float64) string { return fmt.Sprintf("%.1f", baseFontPt*f) }
	return strings.NewReplacer(
		"@window", t.Window, "@sidebar", t.Sidebar, "@surface", t.Surface, "@raised", t.Raised, "@hover", t.Hover, "@border", t.Border, "@line", t.Line,
		"@text3", t.Text3, "@text2", t.Text2, "@text", t.Text,
		"@accenthover", t.AccentHover, "@accentsoft", t.AccentSoft, "@accent", t.Accent, "@onaccent", t.OnAccent,
		"@oksoft", t.OKSoft, "@ok", t.OK, "@warnsoft", t.WarnSoft, "@warn", t.Warn, "@badsoft", t.BadSoft, "@bad", t.Bad, "@infosoft", t.InfoSoft, "@info", t.Info,
		"@fsTitle", size(1.75), "@fsH1", size(1.35), "@fsH2", size(1.12), "@fsSmall", size(0.9),
		"@chevdown", chevDown, "@chevup", chevUp, "@check", check,
	).Replace(styleSheet)
}

// themeMode is the theme asked for: "system", "light" or "dark".
func (a *app) themeMode() string {
	if m := a.m.Config().GUI.Theme; m == "light" || m == "dark" {
		return m
	}
	return "system"
}

// applyTheme switches to the theme asked for (the desktop's own for "system").
func (a *app) applyTheme() {
	dark := a.sysDark
	switch a.themeMode() {
	case "light":
		dark = false
	case "dark":
		dark = true
	}
	if a.themeSet && dark == th.Dark {
		return
	}
	a.themeSet = true
	if dark {
		th = darkTheme
	} else {
		th = lightTheme
	}
	qt.QApplication_SetPalette(qtPalette(th))
	name := "light"
	if dark {
		name = "dark"
	}
	asset := func(icon string, size int, color, suffix string) string {
		p := filepath.Join(a.assets, icon+"-"+name+suffix+".png")
		if _, err := os.Stat(p); err != nil {
			iconPixmap(icon, size, color).Save(p)
		}
		return filepath.ToSlash(p)
	}
	down := asset("chevron-down", 12, th.Text2, "")
	up := asset("chevron-up", 12, th.Text2, "")
	check := asset("check", 13, th.OnAccent, "-on")
	a.qapp.SetStyleSheet(sheet(th, down, up, check))
	resetPaintCache()
	for _, f := range a.themed {
		f()
	}
}

// onTheme runs f now and whenever the theme changes (for what the style sheet can't do: icons
// and colored text).
func (a *app) onTheme(f func()) {
	a.themed = append(a.themed, f)
	f()
}

// qtPalette is a theme as Qt's colors, for the parts a style sheet doesn't reach.
func qtPalette(t theme) *qt.QPalette {
	p := qt.NewQPalette()
	set := func(role qt.QPalette__ColorRole, color string) { p.SetColor2(role, col(color)) }
	set(qt.QPalette__Window, t.Window)
	set(qt.QPalette__WindowText, t.Text)
	set(qt.QPalette__Base, t.Surface)
	set(qt.QPalette__AlternateBase, t.Raised)
	set(qt.QPalette__ToolTipBase, t.Raised)
	set(qt.QPalette__ToolTipText, t.Text)
	set(qt.QPalette__Text, t.Text)
	set(qt.QPalette__Button, t.Surface)
	set(qt.QPalette__ButtonText, t.Text)
	set(qt.QPalette__BrightText, t.Bad)
	set(qt.QPalette__Link, t.Accent)
	set(qt.QPalette__LinkVisited, t.Accent)
	set(qt.QPalette__Highlight, t.Accent)
	set(qt.QPalette__HighlightedText, t.OnAccent)
	set(qt.QPalette__PlaceholderText, t.Text3)
	return p
}

// watchSystemTheme follows the desktop's light or dark mode while the theme is "system".
func (a *app) watchSystemTheme() {
	check := func() {
		go func() {
			ctx, cancel := context.WithTimeout(a.m.Context(), 3*time.Second)
			defer cancel()
			dark, known := systemPrefersDark(ctx)
			if !known {
				return
			}
			mainthread.Start(func() {
				if a.sysDark != dark {
					a.sysDark = dark
					a.applyTheme()
				}
			})
		}()
	}
	timer := qt.NewQTimer2(a.win.QObject)
	timer.OnTimeout(func() {
		if a.themeMode() == "system" {
			check()
		}
	})
	timer.Start(5000)
}

// --- colors, brushes and fonts for drawing ------------------------------------------------------
//
// Qt objects made in Go aren't freed by the garbage collector, and lists are redrawn all the
// time, so what drawing uses is made once.

var (
	colorCache = map[string]*qt.QColor{}
	brushCache = map[string]*qt.QBrush{}
)

func col(hex string) *qt.QColor {
	c := colorCache[hex]
	if c == nil {
		c = qt.NewQColor6(hex)
		colorCache[hex] = c
	}
	return c
}

func brushOf(hex string) *qt.QBrush {
	b := brushCache[hex]
	if b == nil {
		b = qt.NewQBrush3(col(hex))
		brushCache[hex] = b
	}
	return b
}

// resetPaintCache is for a new theme: icons are cached by color, and the old ones are unused.
func resetPaintCache() {
	for k, p := range pixmapCache {
		p.Delete()
		delete(pixmapCache, k)
	}
	for k, ic := range iconCache {
		ic.Delete()
		delete(iconCache, k)
	}
}

type fonts struct {
	normal, bold, small, smallBold *qt.QFont
	mNormal, mBold, mSmall         *qt.QFontMetrics
}

var drawFonts *fonts

func initFonts() {
	base := qt.QApplication_Font()
	if s := base.PointSizeF(); s > 0 {
		baseFontPt = s
	}
	mk := func(scale float64, weight qt.QFont__Weight) *qt.QFont {
		f := qt.NewQFont5(base)
		f.SetPointSizeF(baseFontPt * scale)
		f.SetWeight(weight)
		return f
	}
	f := &fonts{normal: mk(1, qt.QFont__Normal), bold: mk(1, qt.QFont__DemiBold), small: mk(0.9, qt.QFont__Normal), smallBold: mk(0.9, qt.QFont__DemiBold)}
	f.mNormal, f.mBold, f.mSmall = qt.NewQFontMetrics(f.normal), qt.NewQFontMetrics(f.bold), qt.NewQFontMetrics(f.small)
	drawFonts = f
}
