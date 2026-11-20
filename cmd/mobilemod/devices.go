//go:build !darwin

package main

import "go.bug.st/serial/enumerator"

func listPorts() (any, error) { return enumerator.GetDetailedPortsList() }
