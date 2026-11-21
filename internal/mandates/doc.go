// Package mandates is the authority a spending mandate gives (M6N C366-C370): a standing approval the Owner creates in the app, for
// one holder (the night shift), bounded by a budget, an expiry and a scope. It never raises a spend cap: caps are checked first and
// stay the ceiling. Nothing here may create or widen a mandate except an Owner who is a person; revoking one only reduces spend and
// is open to every Owner-side principal.
package mandates
