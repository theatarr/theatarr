package anime

// pySet emulates the iteration order of a CPython 3.10 set of non-negative ints (Objects/setobject.c: hash(i) == i,
// linear probing of 9 slots, then perturbed probing; the table grows to 4× the used count when 3/5 full).
//
// The golden outputs come from code that iterates Python sets in its crawl (who reaches a node first decides its
// kind, direction and "found via") and in the manami link walk. Reproducing them means visiting those nodes in the
// same order, so those sets use this type. Nothing is ever removed from them, so there are no dummy slots.
type pySet struct {
	table []int // 0 = empty; values are stored +1 so that 0 can be a member
	used  int
	fill  int
}

const (
	pySetMinSize   = 8
	pyLinearProbes = 9
	pyPerturbShift = 5
)

func newPySet() *pySet { return &pySet{table: make([]int, pySetMinSize)} }

// pySetOf builds a set the way set(list) and set comprehensions do: one add per item, in order.
func pySetOf(items []int) *pySet {
	s := newPySet()
	for _, v := range items {
		s.Add(v)
	}
	return s
}

func (s *pySet) mask() uint64 { return uint64(len(s.table) - 1) }

// Has reports membership.
func (s *pySet) Has(v int) bool {
	if s == nil {
		return false
	}
	for _, x := range s.table {
		if x == v+1 {
			return true
		}
	}
	return false
}

// Len is the number of members.
func (s *pySet) Len() int {
	if s == nil {
		return 0
	}
	return s.used
}

// Add inserts v (set_add_entry).
func (s *pySet) Add(v int) {
	if v < 0 {
		panic("pySet: negative value")
	}
	mask := s.mask()
	hash := uint64(v)
	i := hash & mask
	if s.table[i] == 0 {
		s.place(i, v)
		return
	}
	perturb := hash
	for {
		if s.table[i] == v+1 {
			return
		}
		if i+pyLinearProbes <= mask {
			for j := uint64(1); j <= pyLinearProbes; j++ {
				x := s.table[i+j]
				if x == 0 {
					s.place(i+j, v)
					return
				}
				if x == v+1 {
					return
				}
			}
		}
		perturb >>= pyPerturbShift
		i = (i*5 + 1 + perturb) & mask
		if s.table[i] == 0 {
			s.place(i, v)
			return
		}
	}
}

func (s *pySet) place(i uint64, v int) {
	s.table[i] = v + 1
	s.fill++
	s.used++
	if uint64(s.fill)*5 < s.mask()*3 {
		return
	}
	minused := s.used * 4
	if s.used > 50000 {
		minused = s.used * 2
	}
	s.resize(minused)
}

// resize is set_table_resize: the new table is the smallest power of two above minused, filled in old slot order.
func (s *pySet) resize(minused int) {
	n := pySetMinSize
	for n <= minused {
		n <<= 1
	}
	old := s.table
	s.table = make([]int, n)
	for _, x := range old {
		if x != 0 {
			insertClean(s.table, x-1)
		}
	}
	s.fill = s.used
}

func insertClean(table []int, v int) {
	mask := uint64(len(table) - 1)
	hash := uint64(v)
	perturb := hash
	i := hash & mask
	for {
		if table[i] == 0 {
			table[i] = v + 1
			return
		}
		if i+pyLinearProbes <= mask {
			for j := uint64(1); j <= pyLinearProbes; j++ {
				if table[i+j] == 0 {
					table[i+j] = v + 1
					return
				}
			}
		}
		perturb >>= pyPerturbShift
		i = (i*5 + 1 + perturb) & mask
	}
}

// Copy is set(other) for a set argument (set_merge into an empty set).
func (s *pySet) Copy() *pySet {
	c := newPySet()
	if s == nil || s.used == 0 {
		return c
	}
	if uint64(s.used)*5 >= c.mask()*3 {
		c.resize(s.used * 2)
	}
	if len(c.table) == len(s.table) && s.fill == s.used {
		copy(c.table, s.table)
		c.fill, c.used = s.fill, s.used
		return c
	}
	for _, x := range s.table {
		if x != 0 {
			insertClean(c.table, x-1)
		}
	}
	c.fill, c.used = s.used, s.used
	return c
}

// Items lists the members in iteration order.
func (s *pySet) Items() []int {
	if s == nil {
		return nil
	}
	out := make([]int, 0, s.used)
	for _, x := range s.table {
		if x != 0 {
			out = append(out, x-1)
		}
	}
	return out
}
