//go:build !windows

package gui

import (
	"context"
	"os/exec"
	"strings"
)

// systemPrefersDark asks the desktop whether it's in dark mode: the settings portal (GNOME,
// KDE, Hyprland and others), then GNOME's settings. known is false when nothing answers.
func systemPrefersDark(ctx context.Context) (dark, known bool) {
	out, err := exec.CommandContext(ctx, "gdbus", "call", "--session",
		"--dest", "org.freedesktop.portal.Desktop", "--object-path", "/org/freedesktop/portal/desktop",
		"--method", "org.freedesktop.portal.Settings.Read", "org.freedesktop.appearance", "color-scheme").Output()
	if err == nil {
		switch s := string(out); {
		case strings.Contains(s, "uint32 1"):
			return true, true
		case strings.Contains(s, "uint32 2"):
			return false, true
		}
	}
	if out, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", "color-scheme").Output(); err == nil {
		switch s := string(out); {
		case strings.Contains(s, "dark"):
			return true, true
		case strings.Contains(s, "light"):
			return false, true
		}
	}
	if out, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", "gtk-theme").Output(); err == nil {
		return strings.Contains(strings.ToLower(string(out)), "dark"), true
	}
	return false, false
}
