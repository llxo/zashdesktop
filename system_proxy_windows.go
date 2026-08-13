//go:build windows

package main

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func systemProxy(request *http.Request) (*url.URL, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return http.ProxyFromEnvironment(request)
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return http.ProxyFromEnvironment(request)
	}
	proxyServer, _, err := key.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(proxyServer) == "" {
		return http.ProxyFromEnvironment(request)
	}
	override, _, _ := key.GetStringValue("ProxyOverride")
	if proxyBypassed(request.URL, override) {
		return nil, nil
	}

	address := proxyAddressForScheme(proxyServer, request.URL.Scheme)
	if address == "" {
		return http.ProxyFromEnvironment(request)
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	proxyURL, err := url.Parse(address)
	if err != nil || proxyURL.Host == "" {
		return http.ProxyFromEnvironment(request)
	}
	return proxyURL, nil
}

func proxyAddressForScheme(proxyServer, scheme string) string {
	defaultAddress := ""
	for _, item := range strings.Split(proxyServer, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			if defaultAddress == "" {
				defaultAddress = item
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[0]), scheme) {
			return strings.TrimSpace(parts[1])
		}
	}
	return defaultAddress
}

func proxyBypassed(target *url.URL, override string) bool {
	host := strings.ToLower(target.Hostname())
	for _, item := range strings.Split(override, ";") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if item == "<local>" && !strings.Contains(host, ".") {
			return true
		}
		item = strings.TrimPrefix(item, "*")
		if strings.HasPrefix(host, item) || strings.HasSuffix(host, item) {
			return true
		}
	}
	return false
}
