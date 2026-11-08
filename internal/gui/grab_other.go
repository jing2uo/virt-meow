//go:build !nogui && !linux

package gui

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// grabKeyboard isn't done on this platform yet: the page falls back to the
// Keyboard Lock API where the webview has it.
func grabKeyboard(*application.App, context.Context, bool) (bool, error) { return false, nil }
