package auth

// DisplayRater is the rater whose row a principal sees as "the stars" in lists, sorts and filters (C307.2). A person sees
// their own row (RaterKey); an Editor or Listener without a user key sees none ("" matches no row, fail closed). Agents,
// connector grants and system are Owner delegates and keep seeing the Owner's stars as before U1 (their own critic row is
// a separate column). Curation signals never use this: they read owner_ratings (D-U1-9).
func DisplayRater(p Principal) string {
	if p.IsPerson() {
		return RaterKey(p)
	}
	return "owner"
}

// NoRater is a rater key that no rating row carries (a comma cannot occur in a user name, D-U1-3). Code whose empty
// rater means "the Owner" uses it to say "nobody" for a person without a rater key.
const NoRater = "no,rater"
