package main

import "regexp"

var validIDPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// validateID checks that a plugin ID matches the required format.
func validateID(id string) bool {
	return id != "" && validIDPattern.MatchString(id)
}
