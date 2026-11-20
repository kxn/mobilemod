package modem

import (
	"os"
	"path/filepath"
	"strings"

	"go.bug.st/serial/enumerator"
)

func DiscoverPorts() ([]string, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	var result []string
	for _, p := range ports {
		if !strings.EqualFold(p.VID, "19d1") || !strings.EqualFold(p.PID, "0001") {
			continue
		}
		path, err := filepath.EvalSymlinks(filepath.Join("/sys/class/tty", filepath.Base(p.Name), "device"))
		if err != nil {
			continue
		}
		if applicationInterface(path) {
			result = append(result, p.Name)
		}
	}
	return result, nil
}

func applicationInterface(path string) bool {
	for path != "/" {
		if b, err := os.ReadFile(filepath.Join(path, "bInterfaceNumber")); err == nil {
			return strings.TrimSpace(string(b)) == "06"
		}
		path = filepath.Dir(path)
	}
	return false
}
