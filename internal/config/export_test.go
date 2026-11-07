package config

// CheckMarker is Marker.Check for a maisys whose current config_version is
// current, so tests can cover reading the previous version.
func CheckMarker(mk *Marker, maisysVersion string, current int) ([]string, error) {
	return mk.check(maisysVersion, current)
}
