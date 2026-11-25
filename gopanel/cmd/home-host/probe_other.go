//go:build !darwin

package main

import "errors"

func probeCable() error {
	return errors.New("physical host probing is supported only on the verified Mac")
}

func inspectRouterVM() error {
	return errors.New("physical host probing is supported only on the verified Mac")
}
func attachWire() error         { return errors.New("physical relay requires the verified Mac") }
func wireService() error        { return errors.New("physical relay requires the verified Mac") }
func installWireService() error { return errors.New("physical relay requires the verified Mac") }
