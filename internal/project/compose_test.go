package project

import "testing"

func TestParseComposePorts(t *testing.T) {
	doc := `
services:
  db:
    image: postgres
    ports:
      - "127.0.0.1:${TRACK_DB_PORT:-5432}:5432"
  api:
    ports:
      - "127.0.0.1:${TRACK_PORT:-5000}:8080"
  web:
    ports:
      - "5173:80"
  udp:
    ports:
      - "5353:5353/udp"
  ephemeral:
    ports:
      - "8080"
  unknown:
    ports:
      - "${NO_DEFAULT}:80"
  long:
    ports:
      - target: 80
        published: "9000"
        protocol: tcp
  ranged:
    ports:
      - "3000-3005:3000-3005"
  ipv6:
    ports:
      - "[::1]:7000:70"
  none:
    image: busybox
`
	got, err := parseComposePorts([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"db": 5432, "api": 5000, "web": 5173, "long": 9000, "ranged": 3000, "ipv6": 7000}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d, want %d", k, got[k], v)
		}
	}
	if _, err := parseComposePorts([]byte("services: [")); err == nil {
		t.Fatal("malformed yaml must error, not yield an empty topology")
	}
}
