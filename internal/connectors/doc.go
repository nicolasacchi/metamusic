// Package connectors owns the hosted-connector grants (C103, C106): the store behind the OAuth-verified
// `agent` principals, the protected-resource metadata handler and, in later tasks, the verifier and the
// limits the spend checks read. It never imports internal/spend (the spend package reads grant limits with
// its own SQL) and never reaches internal/api, internal/gate or internal/httpserver (C116).
package connectors
