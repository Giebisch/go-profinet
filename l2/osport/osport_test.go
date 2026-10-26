// SPDX-License-Identifier: MIT

package osport

import (
	"errors"
	"testing"

	"github.com/otfabric/go-profinet"
)

func TestInterfacesAndMissing(t *testing.T) {
	if _, err := Interfaces(); err != nil {
		t.Fatal(err)
	}
	if Backend() == "" {
		t.Fatal("no backend name")
	}
	if _, err := Open("no-such-interface-xyz", Options{}); !errors.Is(err, profinet.ErrNotFound) && !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatalf("err %v", err)
	}
}
