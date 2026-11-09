package profile

// SeaDexOrder is the one curator preference comparison (configured winner > SeaDex best > other listed
// > ordinary). Only a prefer-on version compares standing, and only differing standing decides: decided says whether
// it did, and order is > 0 when a ranks above b. Ranking, upgrade, queue, approval and import each keep their own
// semantics around it (search or RSS criteria, the actual file quality at import, manual grabs, the prefer-on
// same-torrent guard); none of them compares standing any other way.
func SeaDexOrder(prefer bool, a, b int) (order int, decided bool) {
	if !prefer || a == b {
		return 0, false
	}
	return cmpInt(a, b), true
}
