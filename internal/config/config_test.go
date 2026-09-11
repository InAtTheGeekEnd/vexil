package config

import "testing"

func TestLoad(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Config
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			want: Config{Addr: ":8080", Data: "./data", BaseURL: ""},
		},
		{
			name: "all set",
			env: map[string]string{
				"VEXIL_ADDR":     "127.0.0.1:9000",
				"VEXIL_DATA":     "/var/lib/app",
				"VEXIL_BASE_URL": "https://status.example.com/",
			},
			want: Config{Addr: "127.0.0.1:9000", Data: "/var/lib/app", BaseURL: "https://status.example.com"},
		},
		{
			name: "blank values use defaults",
			env:  map[string]string{"VEXIL_ADDR": "  ", "VEXIL_DATA": ""},
			want: Config{Addr: ":8080", Data: "./data", BaseURL: ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Load(func(k string) (string, bool) {
				v, ok := tt.env[k]
				return v, ok
			})
			if got != tt.want {
				t.Fatalf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
