package main

import "testing"

func TestSerialRequiresOpenWrtShell(t *testing.T) {
	for _, test := range []struct {
		text  string
		ready bool
	}{
		{"grub> ", false},
		{"GNU GRUB: press c for command line", false},
		{"kernel booting\nPlease press Enter to activate this console.", false},
		{"root@OpenWrt:~# ", true},
		{"boot log\r\nroot@OpenWrt:/# ", true},
		{"root@OpenWrt:~# command still running\nconnecting", false},
	} {
		if got := rootPrompt([]byte(test.text)); got != test.ready {
			t.Errorf("%q: ready=%v", test.text, got)
		}
	}
}
