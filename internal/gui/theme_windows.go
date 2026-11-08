//go:build windows

package gui

import (
	"context"

	"golang.org/x/sys/windows/registry"
)

// systemPrefersDark reads Windows' app mode from the registry.
func systemPrefersDark(context.Context) (dark, known bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false, false
	}
	return v == 0, true
}
