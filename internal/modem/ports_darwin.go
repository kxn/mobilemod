package modem

import (
	"os/exec"
	"regexp"
	"strings"
)

// ioreg supplies USB interface metadata without requiring CGO/IOKit bindings.
func DiscoverPorts() ([]string, error) {
	b, err := exec.Command("ioreg", "-r", "-c", "IOUSBHostInterface", "-l", "-w", "0").Output()
	if err != nil {
		return nil, err
	}
	property := regexp.MustCompile(`"(idVendor|idProduct|bInterfaceNumber)" = ([0-9]+)`)
	callout := regexp.MustCompile(`"IOCalloutDevice" = "([^"]+)"`)
	values := map[string]string{}
	var result []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "<class IOUSBHostInterface,") {
			values = map[string]string{}
		}
		if m := property.FindStringSubmatch(line); m != nil {
			values[m[1]] = m[2]
		}
		if m := callout.FindStringSubmatch(line); m != nil && values["idVendor"] == "6609" && values["idProduct"] == "1" && values["bInterfaceNumber"] == "6" {
			result = append(result, m[1])
		}
	}
	return result, nil
}
