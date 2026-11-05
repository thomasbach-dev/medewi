package leva

import (
	"context"
	"testing"
	"time"
)

// The machine can swallow the first MC? even after the banner wait.
func TestQueryXMLNameRetries(t *testing.T) {
	c, s := newPair(t)
	go func() {
		s.readLine(t) // first MC?, swallowed
		s.readLine(t)
		s.write(t, "XML=leva!-x \r\n")
	}()
	name, err := QueryXMLName(context.Background(), c, 3, 100*time.Millisecond, 20*time.Millisecond)
	if err != nil || name != "leva!-x" {
		t.Fatalf("name %q err %v", name, err)
	}
}

func TestQueryXMLNameGivesUp(t *testing.T) {
	c, s := newPair(t)
	go func() {
		for range 2 {
			s.readLine(t)
		}
	}()
	start := time.Now()
	name, err := QueryXMLName(context.Background(), c, 2, 50*time.Millisecond, 10*time.Millisecond)
	if err != nil || name != "" {
		t.Fatalf("name %q err %v, want no name and no error", name, err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Error("tries were not spaced")
	}
}

func TestXMLName(t *testing.T) {
	if n, ok := XMLName([]string{"x", "XML=a.b "}); !ok || n != "a.b" {
		t.Errorf("got %q %v", n, ok)
	}
	if _, ok := XMLName([]string{"x"}); ok {
		t.Error("found a name in none")
	}
}
