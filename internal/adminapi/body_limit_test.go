package adminapi

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadJSONBodyRejectsChunkedBodyBeyondHardLimit(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(maxJSONBodyBytes)+1)
	request := httptest.NewRequest("POST", "/api/v2/test", bytes.NewReader(body))
	request.ContentLength = -1 // exercise the unknown/chunked path
	var value map[string]any
	_, err := readJSONBody(request, &value)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("oversized body error = %v, want %v", err, ErrRequestBodyTooLarge)
	}
}

func TestReadJSONBodyRejectsValidJSONWithWhitespaceTailBeyondLimit(t *testing.T) {
	prefix := []byte(`{"ok":true}`)
	body := append(append([]byte(nil), prefix...), []byte(strings.Repeat(" ", int(maxJSONBodyBytes)-len(prefix)+1))...)
	request := httptest.NewRequest("POST", "/api/v2/test", bytes.NewReader(body))
	request.ContentLength = -1
	var value map[string]any
	_, err := readJSONBody(request, &value)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("whitespace-tail body error = %v, want %v", err, ErrRequestBodyTooLarge)
	}
}

func TestReadJSONBodyAcceptsBodyAtOrBelowHardLimit(t *testing.T) {
	body := []byte(`{"ok":true}`)
	request := httptest.NewRequest("POST", "/api/v2/test", bytes.NewReader(body))
	request.ContentLength = -1
	var value map[string]any
	got, err := readJSONBody(request, &value)
	if err != nil || string(got) != string(body) || value["ok"] != true {
		t.Fatalf("body = %q, value=%#v, err=%v", got, value, err)
	}
}
