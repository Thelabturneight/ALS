package main

import "github.com/veandco/go-sdl2/sdl"

// setClipboardText copia texto para a área de transferência do sistema (SDL).
func setClipboardText(text string) bool {
	return sdl.SetClipboardText(text) == nil
}
