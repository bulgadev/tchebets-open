package handlers

// Teams is the hardcoded head-to-head picker list for admin market creation
// (see AdminCreateMarket) - plain names only, since nothing else in the
// system stores a stable team identity, only the resulting question string
// is ever persisted. Extend this slice directly to add more teams.
var Teams = []string{
	"Trakinas Fc",
	"Legados Fc",
	"Los Quejitos Fc",
	"Vanguarda Fc",
	"Pelegos Fc",
	"Guerreiros do vale Fc",
}

func isKnownTeam(name string) bool {
	for _, t := range Teams {
		if t == name {
			return true
		}
	}
	return false
}
