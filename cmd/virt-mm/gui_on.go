//go:build !nogui

package main

import "github.com/jing2uo/virt-meow/internal/gui"

func runGUI(view string) error { return gui.Run(version, view) }
