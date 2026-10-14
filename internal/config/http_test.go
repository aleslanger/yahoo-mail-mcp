package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestHTTPListenDefaultsToLoopbackAndRejectsOtherHosts(t *testing.T) {
	cases := []struct {
		listen  string
		want    string
		wantErr bool
	}{
		{listen: "", want: "127.0.0.1:8765"},
		{listen: "127.0.0.1:9000", want: "127.0.0.1:9000"},
		{listen: "localhost:9000", want: "localhost:9000"},
		{listen: "[::1]:9000", want: "[::1]:9000"},
		{listen: "0.0.0.0:8765", wantErr: true},
		{listen: "192.168.1.10:8765", wantErr: true},
		{listen: ":8765", wantErr: true},
		{listen: "127.0.0.1:0", wantErr: true},
		{listen: "127.0.0.1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.listen, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(`{"http_listen":"`+tc.listen+`","accounts":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("http_listen %q accepted", tc.listen)
				}
				return
			}
			if err != nil || c.HTTPListen != tc.want {
				t.Fatalf("http_listen = %q, %v; want %q", c.HTTPListen, err, tc.want)
			}
		})
	}
}

func TestHTTPTokenIsCreatedOncePrivatelyAndReadBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := HTTPToken(); err == nil {
		t.Fatal("missing HTTP token accepted")
	}
	token, err := CreateHTTPToken()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
		t.Fatalf("token is not 256-bit hex: %q", token)
	}
	path := filepath.Join(home, ".config", "yahoo-mail-mcp", "http-token")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token file mode: %v, %v", info, err)
	}
	if _, err := CreateHTTPToken(); err == nil {
		t.Fatal("existing HTTP token was overwritten")
	}
	read, err := HTTPToken()
	if err != nil || read != token {
		t.Fatalf("token read back: %q, %v", read, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := HTTPToken(); err == nil {
		t.Fatal("readable HTTP token file accepted")
	}
}
