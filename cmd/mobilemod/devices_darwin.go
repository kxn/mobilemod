//go:build darwin

package main

import "go.bug.st/serial"

// The library's detailed macOS enumeration requires IOKit/CGO. Basic enumeration
// keeps the gateway statically cross-compilable; configured serial I/O is pure Go.
func listPorts() (any, error) { return serial.GetPortsList() }
