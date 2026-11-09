package anime

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestPySetOrder checks the set emulation against iteration orders recorded from CPython 3.10 (testdata/pyset.json,
// made by adding random ints one by one, then copying the set).
func TestPySetOrder(t *testing.T) {
	b, err := os.ReadFile("testdata/pyset.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		In   []int `json:"in"`
		Iter []int `json:"iter"`
		Copy []int `json:"copy"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		s := pySetOf(c.In)
		if got := s.Items(); !reflect.DeepEqual(got, c.Iter) {
			t.Errorf("case %d (%d items): order %v, CPython %v", i, len(c.In), got, c.Iter)
		}
		if got := s.Copy().Items(); !reflect.DeepEqual(got, c.Copy) {
			t.Errorf("case %d: copy order %v, CPython %v", i, got, c.Copy)
		}
	}
}
