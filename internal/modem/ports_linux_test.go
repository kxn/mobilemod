package modem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationInterfaceUsesUSBMetadata(t *testing.T) {
	root := t.TempDir()
	for _, number := range []string{"06", "00", "02"} {
		usb := filepath.Join(root, "usb-interface-"+number)
		tty := filepath.Join(usb, "tty", "arbitrary-port-name")
		if err := os.MkdirAll(tty, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(usb, "bInterfaceNumber"), []byte(number+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if applicationInterface(tty) != (number == "06") {
			t.Fatal("wrong USB interface selected", number)
		}
	}
}
