package api

import "testing/fstest"

func secretFS() fstest.MapFS {
	return fstest.MapFS{
		"etc/config/network": {Data: []byte("config interface 'wg0'\n\toption proto 'wireguard'\n\toption private_key 'UFJJVkFURQ=='\n" +
			"config wireguard_wg0\n\toption public_key 'cGVlcg=='\n\toption preshared_key 'UFJFU0hBUkVE'\n")},
		"etc/config/wireless": {Data: []byte("config wifi-device 'radio0'\n\toption band '2g'\nconfig wifi-iface\n\toption device 'radio0'\n\toption ssid 'home'\n\toption key 'c2VjcmV0LXdpZmk='\n")},
	}
}
