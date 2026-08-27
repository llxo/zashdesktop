package main

import (
	"strings"
	"testing"
)

func TestLaunchURL(t *testing.T) {
	tests := []struct {
		name   string
		config LaunchConfig
		want   string
	}{
		{
			name: "clash API with path and secret",
			config: LaunchConfig{
				APIURL:    "http://127.0.0.1:9090/control",
				APISecret: "top secret",
			},
			want: "/#/setup?hostname=127.0.0.1&http=1&port=9090&secondaryPath=%2Fcontrol&secret=top+secret&type=clash",
		},
		{
			name: "sing-box HTTPS default port",
			config: LaunchConfig{
				APIURL:  "https://localhost",
				APIType: "singbox",
			},
			want: "/#/setup?hostname=localhost&https=1&port=443&secondaryPath=&secret=&type=singbox",
		},
		{
			name: "empty URL opens setup page",
			want: "/",
		},
		{
			name:   "invalid URL opens setup page",
			config: LaunchConfig{APIURL: "://bad"},
			want:   "/",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := launchURL(test.config); got != test.want {
				t.Fatalf("launchURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSecondaryPath(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "", want: ""},
		{path: "/", want: ""},
		{path: "control", want: "/control"},
		{path: "/control", want: "/control"},
	} {
		if got := secondaryPath(test.path); got != test.want {
			t.Errorf("secondaryPath(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestMarshalPersistedCoreProfiles(t *testing.T) {
	profiles := persistedCoreProfiles{
		ActiveCore: "sing-box",
		Behavior: sharedBehaviorConfig{
			RunAsAdmin: true,
			AutoStart:  false,
		},
		Profiles: map[string]CoreConfig{
			"sing-box": {
				CoreType:        "sing-box",
				Version:         "1.11.0",
				Running:         true,
				PID:             1234,
				LogPath:         "C:\\path\\core.log",
				ConfigPath:      "C:\\path\\config.json",
				ConfigAvailable: true,
				CorePath:        "C:\\path\\sing-box.exe",
				RunAsAdmin:      true,
				AutoStart:       false,
			},
		},
	}

	data, err := marshalPersistedCoreProfiles(profiles)
	if err != nil {
		t.Fatalf("marshalPersistedCoreProfiles failed: %v", err)
	}

	jsonStr := string(data)
	forbiddenKeys := []string{
		`"running"`,
		`"pid"`,
		`"logPath"`,
		`"configPath"`,
		`"configAvailable"`,
		`"corePath"`,
		`"configuredVersion"`,
		`"updateAvailable"`,
		`"installedVersion"`,
		`"installed"`,
	}

	for _, key := range forbiddenKeys {
		if strings.Contains(jsonStr, key) {
			t.Errorf("persisted JSON should not contain runtime key %s, got: %s", key, jsonStr)
		}
	}
}

