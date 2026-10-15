//go:build !android

package main

// Android-only bridge; on other platforms there is no wrapper to talk to.
func mobileSend(cmd int, param int) {}
