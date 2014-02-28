package main

import (
	"encoding/json"
	"testing"
)

func TestJSONMarshalling(t *testing.T) {
	a := attachment{
		"application/octet-stream",
		[]byte("some bytes"),
	}
	b, err := json.Marshal(&a)
	if err != nil {
		t.Fatalf("Error marshalling attachment: %v", err)
	}
	t.Logf("Marshalled to %v", string(b))
}
