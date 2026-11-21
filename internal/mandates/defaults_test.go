package mandates

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestService_DefaultsAndUnwired(t *testing.T) {
	t.Parallel()
	s := New(Options{}) // no store, no spend: the defaults and the unwired answers
	if s.o.Now == nil || s.o.NewID == nil {
		t.Fatal("defaults missing")
	}
	if a, b := s.o.NewID(), s.o.NewID(); a == b || len(a) != 36 {
		t.Fatalf("ids %q %q", a, b)
	}
	if _, err := s.Usage(context.Background(), "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Usage: %v", err)
	}
	if s.Location() != time.Local {
		t.Fatalf("location %v", s.Location())
	}
	if (Mandate{HolderKey: "agent:nightshift"}).HolderName() != HolderNightShift || (Mandate{HolderKey: "agent:other"}).HolderName() != "" {
		t.Fatal("HolderName")
	}
}

func TestService_LiveWithoutSpend(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.create()
	s := New(Options{Store: r.st, Now: r.clk.Now}) // a live mandate, but nothing to count its usage with
	if _, _, err := s.Live(context.Background(), r.st.R, "agent:nightshift"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Live: %v", err)
	}
	// without a spend service the validation environment has no batch cap and creating still checks the rest
	if _, err := s.Create(context.Background(), owner, r.input()); err == nil {
		t.Fatal("a second mandate was created while one is live")
	}
}
