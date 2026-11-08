//go:build nogui

package main

import "errors"

func runGUI(string) error { return errors.New("this virt-mm is built without the GUI (-tags nogui)") }
