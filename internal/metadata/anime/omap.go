package anime

// omap is an insertion-ordered map, like a Python dict: overwriting keeps the position, deleting removes it.
type omap[K comparable, V any] struct {
	keys []K
	m    map[K]V
}

func newOmap[K comparable, V any]() *omap[K, V] { return &omap[K, V]{m: map[K]V{}} }

func (o *omap[K, V]) Set(k K, v V) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

// SetDefault sets k only when absent.
func (o *omap[K, V]) SetDefault(k K, v V) {
	if _, ok := o.m[k]; !ok {
		o.Set(k, v)
	}
}

func (o *omap[K, V]) Get(k K) (V, bool) {
	v, ok := o.m[k]
	return v, ok
}

func (o *omap[K, V]) Has(k K) bool {
	_, ok := o.m[k]
	return ok
}

func (o *omap[K, V]) Delete(k K) {
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *omap[K, V]) Len() int { return len(o.keys) }

// Keys returns a copy of the keys in order.
func (o *omap[K, V]) Keys() []K { return append([]K(nil), o.keys...) }
