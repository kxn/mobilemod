package modem

import (
	"strings"

	"go.bug.st/serial/enumerator"
	"golang.org/x/sys/windows/registry"
)

func DiscoverPorts() ([]string, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, p := range ports {
		present[strings.ToUpper(p.Name)] = true
	}
	usb, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Enum\USB`, registry.READ)
	if err != nil {
		return nil, err
	}
	defer usb.Close()
	keys, err := usb.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, key := range keys {
		if !strings.EqualFold(key, "VID_19D1&PID_0001&MI_06") {
			continue
		}
		device, err := registry.OpenKey(usb, key, registry.READ)
		if err != nil {
			continue
		}
		instances, _ := device.ReadSubKeyNames(-1)
		for _, instance := range instances {
			parameters, err := registry.OpenKey(device, instance+`\Device Parameters`, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			port, _, err := parameters.GetStringValue("PortName")
			parameters.Close()
			if err == nil && present[strings.ToUpper(port)] {
				result = append(result, port)
			}
		}
		device.Close()
	}
	return result, nil
}
