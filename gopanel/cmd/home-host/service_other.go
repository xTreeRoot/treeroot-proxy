//go:build !darwin

package main

import "errors"

func installService() error { return errors.New("家庭宿主服务仅适用于已核实的 macOS") }
